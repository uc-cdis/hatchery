package servicemapper

import (
  k8sv1 "k8s.io/api/core/v1"
)

type ServiceMapper interface {
  GetURL(namespace string, serviceName string) (string, error)
  Start(namespace string, serviceName string, userName string, pathRewrite string, useTLS string, srv *k8sv1.Service) error
  Stop(namespace string, serviceName string) error
}
