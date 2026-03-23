package hatchery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/iam"
	k8sv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

const sharedWorkspaceLabelKey = "hatchery-shared-user"

// sharedWorkspaceSAName returns the per-user Kubernetes ServiceAccount name.
func sharedWorkspaceSAName(userName string) string {
	return fmt.Sprintf("hatchery-shared-%s", escapism(userName))
}

// sharedWorkspaceRoleName returns the per-user AWS IAM role name.
func sharedWorkspaceRoleName(userName string) string {
	return fmt.Sprintf("hatchery-shared-%s", escapism(userName))
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
func ensureSharedWorkspaceIAMRole(userName, namespace, bucketName, oidcProviderARN string, prefixes []SharedWorkspacePrefix) (string, error) {
	roleName := sharedWorkspaceRoleName(userName)
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

	bucketARN := fmt.Sprintf(`"arn:aws:s3:::%s"`, bucketName)
	var readOnlyResources, writableResources []string
	for _, p := range prefixes {
		arn := fmt.Sprintf(`"arn:aws:s3:::%s/%s*"`, bucketName, p.Prefix)
		if p.IsReadOnly() {
			readOnlyResources = append(readOnlyResources, arn)
		} else {
			writableResources = append(writableResources, arn)
		}
	}
	statements := []string{fmt.Sprintf(`{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":[%s]}`, bucketARN)}
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
		segment := strings.TrimPrefix(path, pathPrefix)
		if segment == "" {
			continue
		}
		methods := make([]string, len(perms))
		for i, p := range perms {
			methods[i] = p.Method
		}
		prefixes = append(prefixes, SharedWorkspacePrefix{
			Name:        "group-" + segment,
			Prefix:      segment + "/",
			Permissions: methods,
		})
	}
	return prefixes, nil
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

// createSharedWorkspacePVAndPVC creates a statically-provisioned CSI PersistentVolume
// backed by the Mountpoint S3 driver and a PersistentVolumeClaim directly bound to it.
func createSharedWorkspacePVAndPVC(ctx context.Context, podClient corev1.CoreV1Interface, userName, namespace string, prefix SharedWorkspacePrefix) error {
	swCfg := Config.Config.SharedWorkspace
	pvName := sharedPVName(userName, prefix.Name)
	pvcName := sharedPVCName(userName, prefix.Name)
	labels := map[string]string{
		sharedWorkspaceLabelKey: escapism(userName),
	}
	storageSize := resource.MustParse("1Gi")
	storageClass := ""
	volumeHandle := fmt.Sprintf("%s/%s/%s", swCfg.S3BucketName, escapism(userName), escapism(prefix.Name))

	readOnly := prefix.IsReadOnly()
	accessMode := k8sv1.ReadOnlyMany
	mountOptions := []string{fmt.Sprintf("prefix=%s", prefix.Prefix), "read-only", "uid=1010", "gid=100"}
	if !readOnly {
		accessMode = k8sv1.ReadWriteMany
		mountOptions = []string{fmt.Sprintf("prefix=%s", prefix.Prefix), "uid=1010", "gid=100"}
	}

	pv := &k8sv1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:   pvName,
			Labels: labels,
		},
		Spec: k8sv1.PersistentVolumeSpec{
			Capacity: k8sv1.ResourceList{
				k8sv1.ResourceStorage: storageSize,
			},
			AccessModes:                   []k8sv1.PersistentVolumeAccessMode{accessMode},
			PersistentVolumeReclaimPolicy: k8sv1.PersistentVolumeReclaimDelete,
			StorageClassName:              storageClass,
			MountOptions:                  mountOptions,
			PersistentVolumeSource: k8sv1.PersistentVolumeSource{
				CSI: &k8sv1.CSIPersistentVolumeSource{
					Driver:       "s3.csi.aws.com",
					VolumeHandle: volumeHandle,
					VolumeAttributes: map[string]string{
						"bucketName":           swCfg.S3BucketName,
						"authenticationSource": "pod",
						"stsRegion":            "us-east-1",
					},
				},
			},
			ClaimRef: &k8sv1.ObjectReference{
				Kind:      "PersistentVolumeClaim",
				Namespace: namespace,
				Name:      pvcName,
			},
		},
	}

	if _, err := podClient.PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create shared PV %s: %w", pvName, err)
	}

	pvc := &k8sv1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: k8sv1.PersistentVolumeClaimSpec{
			AccessModes:      []k8sv1.PersistentVolumeAccessMode{accessMode},
			StorageClassName: &storageClass,
			VolumeName:       pvName,
			Resources: k8sv1.VolumeResourceRequirements{
				Requests: k8sv1.ResourceList{
					k8sv1.ResourceStorage: storageSize,
				},
			},
		},
	}

	if _, err := podClient.PersistentVolumeClaims(namespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil {
		_ = podClient.PersistentVolumes().Delete(ctx, pvName, metav1.DeleteOptions{})
		return fmt.Errorf("failed to create shared PVC %s: %w", pvcName, err)
	}

	return nil
}

// addSharedWorkspaceVolumesToPod mutates the pod spec in-place, appending one
// PVC-backed volume and one VolumeMount on hatchery-container per prefix.
func addSharedWorkspaceVolumesToPod(pod *k8sv1.Pod, userName string, prefixes []SharedWorkspacePrefix, mountBasePath string) {
	if mountBasePath == "" {
		mountBasePath = "/home/jovyan/shared"
	}
	for _, prefix := range prefixes {
		pvcName := sharedPVCName(userName, prefix.Name)
		volName := fmt.Sprintf("shared-%s", escapism(prefix.Name))
		mountPath := fmt.Sprintf("%s/%s", mountBasePath, prefix.Name)

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
