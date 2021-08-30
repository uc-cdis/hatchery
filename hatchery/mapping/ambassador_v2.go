package mapping

import (
  "fmt"
  "os"
  "path/filepath"
  "context"
  ambassador "github.com/datawire/ambassador/pkg/api/getambassador.io/v2"
  "k8s.io/client-go/rest"
  //"k8s.io/client-go/kubernetes"
  //"k8s.io/client-go/kubernetes/scheme"
  //sigScheme "sigs.k8s.io/controller-runtime/pkg/scheme"
  "k8s.io/apimachinery/pkg/runtime/serializer"
  metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  "k8s.io/apimachinery/pkg/runtime"
  clientcmd "k8s.io/client-go/tools/clientcmd"
  k8sv1 "k8s.io/api/core/v1"
)


type AmbassadorV2Mapper struct {

}


func getMapperClient() (*MappingClient, error) {
  //Some of this code should be centralized into a common util module
	// attempt to create config using $HOME/.kube/config
	home, exists := os.LookupEnv("HOME")
  if !exists {
      home = "/root"
  }
  configPath := filepath.Join(home, ".kube", "config")
  config, err := clientcmd.BuildConfigFromFlags("", configPath)
  if err != nil {
		//if the kube config file is not avalible, use the InCluster config
		config, err = rest.InClusterConfig()
		if err != nil {
      return nil, err
		}
	}
  mappingClient, err := NewMappingClient(config)
  if err != nil {
    return nil, err
  }
  return mappingClient, nil
}

func (a *AmbassadorV2Mapper) Start(userName string, pathRewrite string, useTLS string, srv *k8sv1.Service) error {
  m := &ambassador.Mapping{}
  m.Spec.Prefix = pathRewrite
  m.Spec.Service = srv.ObjectMeta.Name
  m.ObjectMeta.Namespace = srv.ObjectMeta.Namespace
  m.ObjectMeta.Name = srv.ObjectMeta.Name
  client, err := getMapperClient()
  if err != nil {
    return err
  }
  fmt.Printf("Create mapping: %#v\n", m)
  result, err := client.Create(srv.ObjectMeta.Namespace, m)
  if err != nil {
    return err
  }
  fmt.Printf("Started mapping: %#v\n", result)
  return nil
}

func (a *AmbassadorV2Mapper) Stop(namespace string, name string) error {
  return nil
}


type MappingClient struct {
  client *rest.RESTClient
  codec  runtime.ParameterCodec
}


func NewMappingClient(config *rest.Config) (*MappingClient, error) {
  newConfig := *config

  mappingScheme, err := ambassador.SchemeBuilder.Build()
  mappingCodec := runtime.NewParameterCodec(mappingScheme)
  newConfig.GroupVersion = &ambassador.GroupVersion
  newConfig.ContentType = runtime.ContentTypeJSON
  newConfig.NegotiatedSerializer = serializer.NewCodecFactory(mappingScheme)
  newConfig.APIPath = "/apis"

  rClient, err := rest.RESTClientFor(&newConfig)

  return &MappingClient{
    rClient, mappingCodec,
  }, err
}

func (m *MappingClient) List(namespace string) ([]ambassador.Mapping, error) {
  opts := metav1.ListOptions{}
  resultList := &ambassador.MappingList{}
  err := m.client.
        Get().
        Namespace(namespace).
        Resource("mappings").
        VersionedParams(&opts, m.codec).
        Do(context.Background()).
        Into(resultList)
  if err != nil {
    return nil, err
  }
  return resultList.Items, nil
}

func (m *MappingClient) Get(namespace string, name string) (*ambassador.Mapping, error){
  fmt.Printf("URL: %s\n", m.client.Get().
    Namespace(namespace).
    Resource("mappings").
    Name(name).URL())
  result := &ambassador.Mapping{}
  err := m.client.Get().
    Namespace(namespace).
    Resource("mappings").
    Name(name).
    Do(context.Background()).Into(result)
  if err != nil {
    return nil, err
  }
  return result, nil
}

func (m *MappingClient) Create(namespace string, obj *ambassador.Mapping) (*ambassador.Mapping, error) {
	result := &ambassador.Mapping{}
	err := m.client.Post().
		Namespace(namespace).Resource("mappings").
		Body(obj).Do(context.Background()).Into(result)
	return result, err
}

func (m *MappingClient) Delete(namespace string, name string) error {
  options := &metav1.DeleteOptions{}
	return m.client.Delete().
		Namespace(namespace).Resource("mappings").
		Name(name).Body(options).Do(context.Background()).
		Error()
}
