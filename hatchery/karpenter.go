package hatchery

import (
	"context"
	"fmt"
	"os"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	// "sigs.k8s.io/controller-runtime/pkg/client"
)

// Create karpenter AWSNodeTemplate
func createKarpenterAWSNodeTemplate(ctx context.Context, userName string, client dynamic.Interface) error {

	jupyterTemplate, err := getJupyterAWSNodeTemplate(ctx, client)
	if err != nil {
		return err
	}

	// Create a unstructured object.
	u := &unstructured.Unstructured{}

	u.Object = map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": userToResourceName(userName, "pod"),
		},
		"spec": jupyterTemplate.Object["spec"],
	}

	//  Update tags
	u.Object["spec"].(map[string]interface{})["tags"] = map[string]interface{}{
		"Name":                   fmt.Sprintf("eks-%s-jupyter-karpenter", os.Getenv("GEN3_VPCID")),
		"Environment":            os.Getenv("GEN3_VPCID"),
		"Organization":           os.Getenv("GEN3_ENDPOINT"),
		"karpenter.sh/discovery": os.Getenv("GEN3_VPCID"),
		"gen3.io/role":           userToResourceName(userName, "pod"),
		"gen3username":           userToResourceName(userName, "user"),
		"gen3.io/environment":    os.Getenv("GEN3_ENDPOINT"),
	}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "karpenter.k8s.aws",
		Version: "v1alpha1",
		Kind:    "AWSNodeTemplate",
	})

	gvr := schema.GroupVersionResource{
		Group:    "karpenter.k8s.aws",
		Version:  "v1alpha1",
		Resource: "awsnodetemplates",
	}

	// Delete the AWSNodeTemplate if it exists
	err = client.Resource(gvr).Delete(ctx, userToResourceName(userName, "pod"), metav1.DeleteOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			Config.Logger.Print("Error deleting Karpenter AWSNodeTemplate")
			return err
		}
	}
	// Create the AWSNodeTemplate.
	_, err = client.Resource(gvr).Create(ctx, u, metav1.CreateOptions{})
	if err != nil {
		Config.Logger.Print("Error deleting Karpenter AWSNodeTemplate")
		return err
	}
	Config.Logger.Print("Created Karpenter AWSNodeTemplate")
	return nil
}

// // Create karpenter provisioner
func createKarpenterProvisioner(ctx context.Context, userName string, client dynamic.Interface) error {

	jupyterTemplate, err := getJupyterProvisioner(client)
	if err != nil {
		Config.Logger.Print("Error getting Jupyter provisioner: ", err)
		return err
	}

	// // Using an unstructured object.
	u := &unstructured.Unstructured{}
	u.Object = map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": userToResourceName(userName, "pod"),
		},
		"spec": jupyterTemplate.Object["spec"],
	}

	// Update role to match the user
	u.Object["spec"].(map[string]interface{})["labels"] = map[string]interface{}{
		"role": userToResourceName(userName, "pod"),
	}

	// update taints to match users role
	u.Object["spec"].(map[string]interface{})["taints"] = []interface{}{
		map[string]interface{}{
			"key":    "role",
			"value":  userToResourceName(userName, "pod"),
			"effect": "NoSchedule",
		},
	}

	// update providerref
	u.Object["spec"].(map[string]interface{})["providerRef"] = map[string]interface{}{
		"name": userToResourceName(userName, "pod"),
	}

	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "karpenter.sh",
		Kind:    "Provisioner",
		Version: "v1alpha5",
	})

	gvr := schema.GroupVersionResource{
		Group:    "karpenter.sh",
		Version:  "v1alpha5",
		Resource: "provisioners",
	}

	// Check if the provisioner exists already, if it does delete the existing one
	err = client.Resource(gvr).Delete(ctx, userToResourceName(userName, "pod"), metav1.DeleteOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			Config.Logger.Print("Error deleting Karpenter provisioner")
			return err
		}
	}

	// crete the provisioner
	_, err = client.Resource(gvr).Create(ctx, u, metav1.CreateOptions{})
	if err != nil {
		Config.Logger.Print("Error creating Karpenter provisioner inside here")
		return err
	}
	Config.Logger.Printf("Created Karpenter provisioner %s... \n", userToResourceName(userName, "pod"))
	return nil
}

// Delete karpenter provisioner
func deleteKarpenterProvisioner(ctx context.Context, userName string, client dynamic.Interface) error {
	// Delete the provisioner
	err := client.Resource(schema.GroupVersionResource{
		Group:    "karpenter.sh",
		Version:  "v1alpha5",
		Resource: "provisioners",
	}).Delete(ctx, userToResourceName(userName, "pod"), metav1.DeleteOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// Delete karpenter AWSNodeTemplate
func deleteKarpenterAWSNodeTemplate(ctx context.Context, userName string, client dynamic.Interface) error {

	// Delete the AWSNodeTemplate
	err := client.Resource(schema.GroupVersionResource{
		Group:    "karpenter.k8s.aws",
		Version:  "v1alpha1",
		Resource: "awsnodetemplates",
	}).Delete(ctx, userToResourceName(userName, "pod"), metav1.DeleteOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil

}

func getJupyterAWSNodeTemplate(ctx context.Context, client dynamic.Interface) (*unstructured.Unstructured, error) {

	res, err := client.Resource(schema.GroupVersionResource{
		Group:    "karpenter.k8s.aws",
		Version:  "v1alpha1",
		Resource: "awsnodetemplates",
	}).Get(context.Background(), "jupyter", metav1.GetOptions{})
	if err != nil {
		Config.Logger.Print("Error getting Jupyter AWSNodeTemplate: ", err)
		return nil, err
	}

	return res, nil
}

func getJupyterProvisioner(client dynamic.Interface) (*unstructured.Unstructured, error) {

	res, err := client.Resource(schema.GroupVersionResource{
		Group:    "karpenter.sh",
		Version:  "v1alpha5",
		Resource: "provisioners",
	}).Get(context.TODO(), "jupyter", metav1.GetOptions{})
	if err != nil {
		Config.Logger.Print("Error getting Jupyter provisioner: ", err)
		return nil, err
	}
	return res, nil
}

func createKarpenterResources(userName string) error {
	// creates the in-cluster config
	config, err := getKubeConfig()
	if err != nil {
		Config.Logger.Printf("Error creating kubeconfig: %v", err)
		return err
	}
	// create context
	ctx := context.Background()

	// create dynamic client
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		Config.Logger.Printf("Error creating dynamic client: %v", err)
		return err
	}

	err = createKarpenterAWSNodeTemplate(ctx, userName, client)
	if err != nil {
		return err
	}
	err = createKarpenterProvisioner(ctx, userName, client)
	if err != nil {
		return err
	}
	return nil
}

func deleteKarpenterResources(ctx context.Context, userName string, config *rest.Config) error {

	// creates the in-cluster config
	config, err := getKubeConfig()
	if err != nil {
		Config.Logger.Printf("Error creating kubeconfig: %v", err)
		return err
	}

	// create dynamic client
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		Config.Logger.Printf("Error creating dynamic client: %v", err)
		return err
	}
	err = deleteKarpenterAWSNodeTemplate(ctx, userName, client)
	if err != nil {
		return err
	}
	err = deleteKarpenterProvisioner(ctx, userName, client)
	if err != nil {
		return err
	}
	return nil
}
