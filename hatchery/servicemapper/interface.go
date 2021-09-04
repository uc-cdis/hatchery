package servicemapper

import (
  k8sv1 "k8s.io/api/core/v1"
)


type ServiceMapper interface {
  Start(userName string, pathRewrite string, useTLS string, srv *k8sv1.Service) error
  Stop(namespace string, name string) error
}
