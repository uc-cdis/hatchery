package hatchery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/iam"
	"github.com/aws/aws-sdk-go/service/iam/iamiface"
	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// policyDoc is the shape of an IAM policy document, used to assert the generated
// JSON is well-formed rather than just string-matching it.
type policyDoc struct {
	Version   string `json:"Version"`
	Statement []struct {
		Effect    string          `json:"Effect"`
		Action    []string        `json:"Action"`
		Resource  []string        `json:"Resource"`
		Condition json.RawMessage `json:"Condition"`
	} `json:"Statement"`
}

func parsePolicy(t *testing.T, doc string) policyDoc {
	t.Helper()
	var parsed policyDoc
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatalf("policy is not valid JSON: %v\ndocument: %s", err, doc)
	}
	for i, stmt := range parsed.Statement {
		if len(stmt.Resource) == 0 {
			t.Errorf("statement %d has an empty Resource list, which AWS rejects: %s", i, doc)
		}
	}
	return parsed
}

func TestBuildWorkspaceS3Policy(t *testing.T) {
	readOnly := SharedWorkspacePrefix{
		Name: "group-a", BucketName: "shared-bucket", Prefix: "group-a/", Permissions: []string{"read"},
	}
	writable := SharedWorkspacePrefix{
		Name: "group-b", BucketName: "shared-bucket", Prefix: "group-b/", Permissions: []string{"read", "write"},
	}
	library := &softwareLibraryAccess{BucketName: "library-bucket", Prefix: "software-library/"}

	t.Run("libraryOnly", func(t *testing.T) {
		// The squashfs-only case: no prefixes at all. This is the combination that
		// used to emit an empty Resource list and get rejected by AWS.
		doc := buildWorkspaceS3Policy(nil, library)
		parsed := parsePolicy(t, doc)

		if len(parsed.Statement) != 2 {
			t.Errorf("expected 2 statements (list + get), got %d: %s", len(parsed.Statement), doc)
		}
		if strings.Contains(doc, "s3:PutObject") || strings.Contains(doc, "s3:DeleteObject") {
			t.Errorf("software library access must be read-only, got: %s", doc)
		}
		if !strings.Contains(doc, `"arn:aws:s3:::library-bucket/software-library/*"`) {
			t.Errorf("expected object ARN scoped to the prefix, got: %s", doc)
		}
		// ListBucket is a bucket-level action, so the prefix must be expressed as a
		// condition; a prefixed resource ARN would deny listing entirely.
		if !strings.Contains(doc, `"s3:prefix":["software-library/*"]`) {
			t.Errorf("expected s3:prefix condition on ListBucket, got: %s", doc)
		}
		for _, stmt := range parsed.Statement {
			for _, res := range stmt.Resource {
				if res == "arn:aws:s3:::library-bucket" && stmt.Condition == nil {
					t.Errorf("bucket-level ListBucket should carry an s3:prefix condition: %s", doc)
				}
			}
		}
	})

	t.Run("libraryOnlyNoPrefix", func(t *testing.T) {
		doc := buildWorkspaceS3Policy(nil, &softwareLibraryAccess{BucketName: "library-bucket"})
		parsePolicy(t, doc)

		// Whole-bucket listing: no condition to apply.
		if strings.Contains(doc, "s3:prefix") {
			t.Errorf("did not expect an s3:prefix condition for a root-mounted bucket: %s", doc)
		}
		if !strings.Contains(doc, `"arn:aws:s3:::library-bucket/*"`) {
			t.Errorf("expected whole-bucket object ARN, got: %s", doc)
		}
	})

	t.Run("sharedWorkspaceOnly", func(t *testing.T) {
		doc := buildWorkspaceS3Policy([]SharedWorkspacePrefix{readOnly, writable}, nil)
		parsePolicy(t, doc)

		if strings.Contains(doc, "library-bucket") {
			t.Errorf("library statements must not appear when the library is unused: %s", doc)
		}
		if !strings.Contains(doc, "s3:PutObject") {
			t.Errorf("expected write actions for the writable prefix: %s", doc)
		}
		// The bucket appears in both prefixes but should be listed once.
		if got := strings.Count(doc, `"arn:aws:s3:::shared-bucket"`); got != 1 {
			t.Errorf("expected the bucket ARN once, got %d: %s", got, doc)
		}
	})

	t.Run("both", func(t *testing.T) {
		doc := buildWorkspaceS3Policy([]SharedWorkspacePrefix{readOnly, writable}, library)
		parsePolicy(t, doc)

		if !strings.Contains(doc, "library-bucket") {
			t.Errorf("expected library access: %s", doc)
		}
		if !strings.Contains(doc, "shared-bucket") {
			t.Errorf("expected shared workspace access: %s", doc)
		}
		if !strings.Contains(doc, "s3:PutObject") {
			t.Errorf("expected write actions for the writable prefix: %s", doc)
		}
	})

	t.Run("neither", func(t *testing.T) {
		// Degenerate case; the caller avoids it, but it must not be malformed.
		doc := buildWorkspaceS3Policy(nil, nil)
		parsed := parsePolicy(t, doc)
		if len(parsed.Statement) != 0 {
			t.Errorf("expected no statements, got %d: %s", len(parsed.Statement), doc)
		}
	})

	t.Run("emptyLibraryBucketIgnored", func(t *testing.T) {
		doc := buildWorkspaceS3Policy(nil, &softwareLibraryAccess{})
		parsed := parsePolicy(t, doc)
		if len(parsed.Statement) != 0 {
			t.Errorf("a library with no bucket should contribute nothing, got: %s", doc)
		}
	})
}

// stubIAM records the calls made against it. Embedding iamiface.IAMAPI means only
// the three methods actually used need implementations.
type stubIAM struct {
	iamiface.IAMAPI
	roleExists      bool
	getRoleCalls    int
	createRoleCalls int
	putPolicyCalls  int
	lastPolicyDoc   string
	lastTrustPolicy string
	putPolicyErr    error
	createRoleErr   error
}

func (s *stubIAM) GetRole(*iam.GetRoleInput) (*iam.GetRoleOutput, error) {
	s.getRoleCalls++
	if s.roleExists {
		return &iam.GetRoleOutput{}, nil
	}
	return nil, awserr.New(iam.ErrCodeNoSuchEntityException, "not found", nil)
}

func (s *stubIAM) CreateRole(in *iam.CreateRoleInput) (*iam.CreateRoleOutput, error) {
	s.createRoleCalls++
	if in.AssumeRolePolicyDocument != nil {
		s.lastTrustPolicy = *in.AssumeRolePolicyDocument
	}
	if s.createRoleErr != nil {
		return nil, s.createRoleErr
	}
	return &iam.CreateRoleOutput{}, nil
}

func (s *stubIAM) PutRolePolicy(in *iam.PutRolePolicyInput) (*iam.PutRolePolicyOutput, error) {
	s.putPolicyCalls++
	if in.PolicyDocument != nil {
		s.lastPolicyDoc = *in.PolicyDocument
	}
	if s.putPolicyErr != nil {
		return nil, s.putPolicyErr
	}
	return &iam.PutRolePolicyOutput{}, nil
}

const testOIDCARN = "arn:aws:iam::123456789012:oidc-provider/oidc.eks.us-east-1.amazonaws.com/id/EXAMPLE"

func TestEnsureWorkspaceIAMRoleWithClient(t *testing.T) {
	defer SetupAndTeardownTest()()
	library := &softwareLibraryAccess{BucketName: "library-bucket", Prefix: "software-library/"}

	t.Run("createsRoleWhenMissing", func(t *testing.T) {
		svc := &stubIAM{roleExists: false}
		arn, err := ensureWorkspaceIAMRoleWithClient(svc, "bob", "jupyter-pods", testOIDCARN, nil, library)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if svc.createRoleCalls != 1 {
			t.Errorf("expected CreateRole once, got %d", svc.createRoleCalls)
		}
		if svc.putPolicyCalls != 1 {
			t.Errorf("expected PutRolePolicy once, got %d", svc.putPolicyCalls)
		}
		// escapism() rewrites "-" as "-2d", so the namespace is not literal here.
		if want := "arn:aws:iam::123456789012:role/hatchery-shared-jupyter-2dpods-bob"; arn != want {
			t.Errorf("expected role ARN %q, got %q", want, arn)
		}
		// The trust policy must pin the SA that ensureWorkspaceServiceAccount creates.
		if !strings.Contains(svc.lastTrustPolicy, "system:serviceaccount:jupyter-pods:hatchery-shared-bob") {
			t.Errorf("trust policy does not pin the workspace service account: %s", svc.lastTrustPolicy)
		}
	})

	t.Run("skipsCreateWhenRoleExistsButStillConvergesPolicy", func(t *testing.T) {
		svc := &stubIAM{roleExists: true}
		if _, err := ensureWorkspaceIAMRoleWithClient(svc, "bob", "jupyter-pods", testOIDCARN, nil, library); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if svc.createRoleCalls != 0 {
			t.Errorf("expected no CreateRole for an existing role, got %d", svc.createRoleCalls)
		}
		// Unconditional PutRolePolicy is what lets existing roles pick up the new
		// library statements on the user's next launch.
		if svc.putPolicyCalls != 1 {
			t.Errorf("expected PutRolePolicy once, got %d", svc.putPolicyCalls)
		}
		if !strings.Contains(svc.lastPolicyDoc, "library-bucket") {
			t.Errorf("expected library access in the converged policy: %s", svc.lastPolicyDoc)
		}
	})

	t.Run("propagatesPutPolicyError", func(t *testing.T) {
		svc := &stubIAM{roleExists: true, putPolicyErr: fmt.Errorf("simulated failure")}
		if _, err := ensureWorkspaceIAMRoleWithClient(svc, "bob", "jupyter-pods", testOIDCARN, nil, library); err == nil {
			t.Error("expected an error when PutRolePolicy fails")
		}
	})
}

func TestEnsureWorkspaceServiceAccount(t *testing.T) {
	defer SetupAndTeardownTest()()
	const ns = "jupyter-pods"
	const roleARN = "arn:aws:iam::123456789012:role/hatchery-shared-jupyter-pods-bob"

	t.Run("createsWhenMissing", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		if err := ensureWorkspaceServiceAccount(context.Background(), clientset.CoreV1(), ns, "bob", roleARN); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		sa, err := clientset.CoreV1().ServiceAccounts(ns).Get(context.Background(), "hatchery-shared-bob", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("expected the service account to exist: %v", err)
		}
		if got := sa.Annotations["eks.amazonaws.com/role-arn"]; got != roleARN {
			t.Errorf("expected role annotation %q, got %q", roleARN, got)
		}
	})

	t.Run("updatesExistingAndPreservesOtherAnnotations", func(t *testing.T) {
		clientset := fake.NewSimpleClientset(&k8sv1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "hatchery-shared-bob",
				Namespace:   ns,
				Annotations: map[string]string{"unrelated": "keep-me", "eks.amazonaws.com/role-arn": "arn:aws:iam::123456789012:role/stale"},
			},
		})
		if err := ensureWorkspaceServiceAccount(context.Background(), clientset.CoreV1(), ns, "bob", roleARN); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		sa, err := clientset.CoreV1().ServiceAccounts(ns).Get(context.Background(), "hatchery-shared-bob", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("expected the service account to exist: %v", err)
		}
		if got := sa.Annotations["eks.amazonaws.com/role-arn"]; got != roleARN {
			t.Errorf("expected the stale role annotation to be replaced, got %q", got)
		}
		if got := sa.Annotations["unrelated"]; got != "keep-me" {
			t.Errorf("expected unrelated annotations to survive, got %q", got)
		}
	})

	t.Run("surfacesNonNotFoundGetError", func(t *testing.T) {
		clientset := fake.NewSimpleClientset()
		clientset.PrependReactor("get", "serviceaccounts", func(action k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, fmt.Errorf("simulated API failure")
		})
		// A transient error must not be mistaken for "does not exist" and turned
		// into a confusing AlreadyExists from the create path.
		if err := ensureWorkspaceServiceAccount(context.Background(), clientset.CoreV1(), ns, "bob", roleARN); err == nil {
			t.Error("expected the API error to be surfaced")
		}
	})
}

func TestResolveSoftwareLibraryBucket(t *testing.T) {
	defer SetupAndTeardownTest()()

	t.Run("perContainerOverrideWins", func(t *testing.T) {
		Config.Config.S3Config = S3Config{BucketName: "global-bucket", Region: "us-west-2"}
		bucket, region, prefix, err := resolveSoftwareLibraryBucket(SquashFSMountConfig{
			BucketName: "container-bucket", Region: "eu-west-1", BucketPrefix: "/lib/",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bucket != "container-bucket" || region != "eu-west-1" {
			t.Errorf("expected the container override to win, got %q / %q", bucket, region)
		}
		if prefix != "lib/" {
			t.Errorf("expected a normalized prefix %q, got %q", "lib/", prefix)
		}
	})

	t.Run("fallsBackToS3Config", func(t *testing.T) {
		Config.Config.S3Config = S3Config{BucketName: "global-bucket", Region: "us-west-2"}
		bucket, region, prefix, err := resolveSoftwareLibraryBucket(SquashFSMountConfig{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bucket != "global-bucket" || region != "us-west-2" {
			t.Errorf("expected the s3-config fallback, got %q / %q", bucket, region)
		}
		if prefix != "" {
			t.Errorf("expected an empty prefix, got %q", prefix)
		}
	})

	t.Run("defaultsRegionWhenUnset", func(t *testing.T) {
		Config.Config.S3Config = S3Config{BucketName: "global-bucket"}
		_, region, _, err := resolveSoftwareLibraryBucket(SquashFSMountConfig{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if region != defaultSTSRegion {
			t.Errorf("expected the region to default to %q, got %q", defaultSTSRegion, region)
		}
	})

	t.Run("errorsWhenNoBucketAnywhere", func(t *testing.T) {
		Config.Config.S3Config = S3Config{}
		if _, _, _, err := resolveSoftwareLibraryBucket(SquashFSMountConfig{}); err == nil {
			t.Error("expected an error when no bucket is configured")
		}
	})
}

func TestResolveOIDCProviderARN(t *testing.T) {
	defer SetupAndTeardownTest()()

	t.Run("prefersTopLevel", func(t *testing.T) {
		Config.Config.OIDCProviderARN = "arn:top-level"
		Config.Config.SharedWorkspace.OIDCProviderARN = "arn:legacy"
		if got := resolveOIDCProviderARN(); got != "arn:top-level" {
			t.Errorf("expected the top-level ARN, got %q", got)
		}
	})

	t.Run("fallsBackToSharedWorkspace", func(t *testing.T) {
		Config.Config.OIDCProviderARN = ""
		Config.Config.SharedWorkspace.OIDCProviderARN = "arn:legacy"
		if got := resolveOIDCProviderARN(); got != "arn:legacy" {
			t.Errorf("expected the legacy ARN, got %q", got)
		}
	})

	t.Run("emptyWhenNeitherSet", func(t *testing.T) {
		Config.Config.OIDCProviderARN = ""
		Config.Config.SharedWorkspace.OIDCProviderARN = ""
		if got := resolveOIDCProviderARN(); got != "" {
			t.Errorf("expected an empty ARN, got %q", got)
		}
	})
}
