package hatchery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/iam"
	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

const sharedWorkspaceLabelKey = "hatchery-shared-user"

// sharedWorkspaceSAName returns the per-user Kubernetes ServiceAccount name.
func sharedWorkspaceSAName(userName string) string {
	return fmt.Sprintf("hatchery-shared-%s", escapism(userName))
}

// sharedWorkspaceRoleName returns the per-user AWS IAM role name.
func sharedWorkspaceRoleName(namespace string, userName string) string {
	return fmt.Sprintf("hatchery-shared-%s-%s", escapism(namespace), escapism(userName))
}

// oidcProviderID returns the host portion of the OIDC provider ARN,
// e.g. "oidc.eks.us-east-1.amazonaws.com/id/EXAMPLED539D4633E53DE1B716D3041E".
func oidcProviderID(providerARN string) string {
	parts := strings.SplitN(providerARN, ":oidc-provider/", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return ""
}

// accountIDFromOIDCARN parses the AWS account ID embedded in the OIDC provider ARN.
func accountIDFromOIDCARN(providerARN string) string {
	// arn:aws:iam::ACCOUNT_ID:oidc-provider/...
	parts := strings.Split(providerARN, ":")
	if len(parts) >= 5 {
		return parts[4]
	}
	return ""
}

// ensureSharedWorkspaceIAMRole creates (or updates the inline policy of) a
// per-user IAM role whose S3 policy is scoped to exactly the given prefixes.
// Returns the role ARN.
func ensureSharedWorkspaceIAMRole(userName, namespace, oidcProviderARN string, prefixes []SharedWorkspacePrefix) (string, error) {
	roleName := sharedWorkspaceRoleName(namespace, userName)
	accountID := accountIDFromOIDCARN(oidcProviderARN)
	providerID := oidcProviderID(oidcProviderARN)
	saName := sharedWorkspaceSAName(userName)

	trustPolicy := fmt.Sprintf(`{
		"Version": "2012-10-17",
		"Statement": [{
			"Effect": "Allow",
			"Principal": {"Federated": %q},
			"Action": "sts:AssumeRoleWithWebIdentity",
			"Condition": {"StringEquals": {
				%q: "system:serviceaccount:%s:%s",
				%q: "sts.amazonaws.com"
			}}
		}]
	}`,
		oidcProviderARN,
		providerID+":sub", namespace, saName,
		providerID+":aud",
	)

	seenBuckets := map[string]bool{}
	var bucketARNs []string
	var readOnlyResources, writableResources []string
	for _, p := range prefixes {
		if !seenBuckets[p.BucketName] {
			seenBuckets[p.BucketName] = true
			bucketARNs = append(bucketARNs, fmt.Sprintf(`"arn:aws:s3:::%s"`, p.BucketName))
		}
		arn := fmt.Sprintf(`"arn:aws:s3:::%s/%s*"`, p.BucketName, p.Prefix)
		if p.IsReadOnly() {
			readOnlyResources = append(readOnlyResources, arn)
		} else {
			writableResources = append(writableResources, arn)
		}
	}
	statements := []string{fmt.Sprintf(`{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":[%s]}`, strings.Join(bucketARNs, ","))}
	if len(readOnlyResources) > 0 {
		statements = append(statements, fmt.Sprintf(`{"Effect":"Allow","Action":["s3:GetObject"],"Resource":[%s]}`, strings.Join(readOnlyResources, ",")))
	}
	if len(writableResources) > 0 {
		statements = append(statements, fmt.Sprintf(`{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:AbortMultipartUpload"],"Resource":[%s]}`, strings.Join(writableResources, ",")))
	}
	s3Policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[%s]}`, strings.Join(statements, ","))

	svc := iam.New(session.Must(session.NewSession()))

	_, err := svc.GetRole(&iam.GetRoleInput{RoleName: aws.String(roleName)})
	if err != nil {
		aerr, ok := err.(awserr.Error)
		if !ok || aerr.Code() != iam.ErrCodeNoSuchEntityException {
			return "", fmt.Errorf("failed to check IAM role %s: %w", roleName, err)
		}

		if _, err = svc.CreateRole(&iam.CreateRoleInput{
			RoleName:                 aws.String(roleName),
			AssumeRolePolicyDocument: aws.String(trustPolicy),
		}); err != nil {
			return "", fmt.Errorf("failed to create IAM role %s: %w", roleName, err)
		}
	}

	if _, err = svc.PutRolePolicy(&iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String("shared-workspace-s3"),
		PolicyDocument: aws.String(s3Policy),
	}); err != nil {
		return "", fmt.Errorf("failed to put inline policy for IAM role %s: %w", roleName, err)
	}

	return fmt.Sprintf("arn:aws:iam::%s:role/%s", accountID, roleName), nil
}

// ensureSharedWorkspaceServiceAccount creates or updates the per-user Kubernetes
// ServiceAccount annotated with the IAM role ARN for IRSA.
func ensureSharedWorkspaceServiceAccount(ctx context.Context, podClient corev1.CoreV1Interface, namespace, userName, roleARN string) error {
	saName := sharedWorkspaceSAName(userName)
	existing, err := podClient.ServiceAccounts(namespace).Get(ctx, saName, metav1.GetOptions{})
	if err != nil {
		sa := &k8sv1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      saName,
				Namespace: namespace,
				Annotations: map[string]string{
					"eks.amazonaws.com/role-arn": roleARN,
				},
			},
		}
		_, err = podClient.ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{})
		return err
	}
	if existing.Annotations == nil {
		existing.Annotations = make(map[string]string)
	}
	existing.Annotations["eks.amazonaws.com/role-arn"] = roleARN
	_, err = podClient.ServiceAccounts(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// SharedWorkspacePrefix represents one shared workspace the user can access.
type SharedWorkspacePrefix struct {
	Name        string   `json:"name"`
	BucketName  string   `json:"bucket-name"`
	Prefix      string   `json:"prefix"`
	Permissions []string `json:"permissions"`
}

// IsReadOnly returns true unless Fence granted the "write" method.
func (p SharedWorkspacePrefix) IsReadOnly() bool {
	for _, perm := range p.Permissions {
		if strings.EqualFold(perm, "write") {
			return false
		}
	}
	return true
}

type fenceAuthzEntry struct {
	Method  string `json:"method"`
	Service string `json:"service"`
}

type fenceUserResponse struct {
	Authz map[string][]fenceAuthzEntry `json:"authz"`
}

// sharedPVName returns the cluster-scoped PersistentVolume name for a user+prefix pair.
func sharedPVName(userName, prefixName string) string {
	return fmt.Sprintf("shared-%s-%s", escapism(userName), escapism(prefixName))
}

// sharedPVCName returns the PersistentVolumeClaim name for a user+prefix pair.
func sharedPVCName(userName, prefixName string) string {
	return fmt.Sprintf("shared-claim-%s-%s", escapism(userName), escapism(prefixName))
}

// shortenedVolumeName returns the volume name hashed version.
func shortenedVolumeName(prefix string, name string) string {
	sum := sha256.Sum256([]byte(name))
	hash := hex.EncodeToString(sum[:])[:16]
	out := fmt.Sprintf("%s-%s", prefix, hash)
	return out
}

// sharedVolName returns the volume name or a hashed version.
func sharedVolName(name string) string {
	old := fmt.Sprintf("shared-%s", escapism(name))
	if len(k8svalidation.IsDNS1123Label(old)) == 0 {
		return old
	}
	return shortenedVolumeName("shared", name)
}

// getSharedWorkspacePrefixes calls the configured external API with the user's
// bearer token and returns the list of S3 prefixes the user may access.
func getSharedWorkspacePrefixes(ctx context.Context, accessToken string) ([]SharedWorkspacePrefix, error) {
	endpoint := "http://fence-service/user"
	resp, err := MakeARequestWithContext(ctx, "GET", endpoint, accessToken, "application/json", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("shared workspace API request failed: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			Config.Logger.Printf("Error closing shared workspace API response body: %v", err)
		}
	}()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("shared workspace API returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read shared workspace API response: %w", err)
	}

	var fenceResp fenceUserResponse
	if err := json.Unmarshal(body, &fenceResp); err != nil {
		return nil, fmt.Errorf("failed to decode shared workspace API response: %w", err)
	}

	const pathPrefix = "/shared/workspace/"
	var prefixes []SharedWorkspacePrefix

	Config.Logger.Printf("%s", fenceResp.Authz)

	for path, perms := range fenceResp.Authz {
		if !strings.HasPrefix(path, pathPrefix) {
			continue
		}
		// Expected format: /shared/workspace/<bucket>/<group>/
		remainder := strings.Trim(strings.TrimPrefix(path, pathPrefix), "/")
		parts := strings.SplitN(remainder, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			Config.Logger.Printf("Warning: skipping shared workspace path with unexpected format: %s", path)
			continue
		}
		bucketName := parts[0]
		group := parts[1]
		if strings.Contains(group, "//") || strings.HasPrefix(group, "/") {
			Config.Logger.Printf("Warning: skipping shared workspace path with empty segments: %s", path)
			continue
		}
		methods := make([]string, len(perms))
		for i, p := range perms {
			methods[i] = p.Method
		}
		safeName := strings.ReplaceAll(group, "/", "-")
		prefixes = append(prefixes, SharedWorkspacePrefix{
			Name:        "group-" + safeName,
			BucketName:  bucketName,
			Prefix:      group + "/",
			Permissions: methods,
		})
	}
	return deduplicatePrefixes(prefixes), nil
}

// deduplicatePrefixes removes child prefixes that are already fully covered by
// an ancestor prefix with equal or higher permissions. A writable ancestor
// covers any child; a read-only ancestor covers a read-only child but not a
// writable one (which requires its own mount to expose write access).
func deduplicatePrefixes(prefixes []SharedWorkspacePrefix) []SharedWorkspacePrefix {
	var result []SharedWorkspacePrefix
	for _, p := range prefixes {
		covered := false
		for _, other := range prefixes {
			if other.BucketName != p.BucketName || other.Prefix == p.Prefix {
				continue
			}
			if strings.HasPrefix(p.Prefix, other.Prefix) {
				if !other.IsReadOnly() || p.IsReadOnly() {
					covered = true
					break
				}
			}
		}
		if !covered {
			result = append(result, p)
		}
	}
	return result
}

// cleanupUserSharedWorkspaces deletes all shared PVCs (and their bound PVs) for
// a user that were created in a prior launch. Failures are logged but not returned,
// so stale resources do not block a new launch.
func cleanupUserSharedWorkspaces(ctx context.Context, podClient corev1.CoreV1Interface, userName, namespace string) error {
	labelSelector := fmt.Sprintf("%s=%s", sharedWorkspaceLabelKey, escapism(userName))
	pvcs, err := podClient.PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return fmt.Errorf("failed to list shared PVCs for user %s: %w", userName, err)
	}
	policy := metav1.DeletePropagationBackground
	deleteOpts := metav1.DeleteOptions{PropagationPolicy: &policy}
	for _, pvc := range pvcs.Items {
		pvName := pvc.Spec.VolumeName
		if err := podClient.PersistentVolumeClaims(namespace).Delete(ctx, pvc.Name, deleteOpts); err != nil {
			Config.Logger.Printf("Warning: failed to delete shared PVC %s for user %s: %v (continuing)", pvc.Name, userName, err)
		}
		if pvName != "" {
			if err := podClient.PersistentVolumes().Delete(ctx, pvName, deleteOpts); err != nil {
				Config.Logger.Printf("Warning: failed to delete shared PV %s for user %s: %v (continuing)", pvName, userName, err)
			}
		}
	}
	return nil
}

// createSharedWorkspacePVAndPVC creates a statically-provisioned Mountpoint-S3
// PersistentVolume and PersistentVolumeClaim for the given user and prefix.
func createSharedWorkspacePVAndPVC(ctx context.Context, podClient corev1.CoreV1Interface, userName, namespace string, prefix SharedWorkspacePrefix) error {
	accessMode := k8sv1.ReadOnlyMany
	mountOptions := []string{fmt.Sprintf("prefix=%s", prefix.Prefix), "read-only", "uid=1010", "gid=100"}
	if !prefix.IsReadOnly() {
		accessMode = k8sv1.ReadWriteMany
		mountOptions = []string{fmt.Sprintf("prefix=%s", prefix.Prefix), "uid=1010", "gid=100", "allow-overwrite", "allow-delete"}
	}
	return createMountpointS3PVAndPVC(ctx, podClient, mountpointS3PVSpec{
		PVName:       sharedPVName(userName, prefix.Name),
		PVCName:      sharedPVCName(userName, prefix.Name),
		Namespace:    namespace,
		Labels:       map[string]string{sharedWorkspaceLabelKey: escapism(userName)},
		BucketName:   prefix.BucketName,
		VolumeHandle: fmt.Sprintf("%s/%s/%s", prefix.BucketName, escapism(userName), escapism(prefix.Name)),
		MountOptions: mountOptions,
		AccessMode:   accessMode,
	})
}

// addSharedWorkspaceVolumesToPod mutates the pod spec in-place, appending one
// PVC-backed volume and one VolumeMount on hatchery-container per prefix.
func addSharedWorkspaceVolumesToPod(pod *k8sv1.Pod, userName string, prefixes []SharedWorkspacePrefix, mountBasePath string) {
	if mountBasePath == "" {
		mountBasePath = "/home/jovyan/shared"
	}
	for _, prefix := range prefixes {
		pvcName := sharedPVCName(userName, prefix.Name)
		volName := sharedVolName(prefix.Name)
		relativePrefixPath := strings.Trim(prefix.Prefix, "/")
		if relativePrefixPath == "" {
			relativePrefixPath = prefix.Name
		}
		mountPath := path.Join(mountBasePath, relativePrefixPath)

		pod.Spec.Volumes = append(pod.Spec.Volumes, k8sv1.Volume{
			Name: volName,
			VolumeSource: k8sv1.VolumeSource{
				PersistentVolumeClaim: &k8sv1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
					ReadOnly:  prefix.IsReadOnly(),
				},
			},
		})

		for i := range pod.Spec.Containers {
			if pod.Spec.Containers[i].Name == "hatchery-container" {
				pod.Spec.Containers[i].VolumeMounts = append(
					pod.Spec.Containers[i].VolumeMounts,
					k8sv1.VolumeMount{
						Name:      volName,
						MountPath: mountPath,
						ReadOnly:  prefix.IsReadOnly(),
					},
				)
				break
			}
		}
	}
}
