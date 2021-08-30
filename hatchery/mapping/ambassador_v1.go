package mapping

import (
  "bytes"
  "text/template"
  "github.com/ghodss/yaml"

  k8sv1 "k8s.io/api/core/v1"
)


type AmbassadorV1Mapper struct {
  HostDomain     string           `json:"host-domain"`
	MappingTemplate *map[string]interface{} `json:"mapping-template"`
}


const defaultAmbassadorYaml = `---
apiVersion: ambassador/v1
kind:  Mapping
name:  {{.resourceName}}
prefix: /
headers:
  remote_user: {{.userName}}
service: {{.serviceName}}.{{.nameSpace}}.{{.hostDomain}}
bypass_auth: true
timeout_ms: 300000
use_websocket: true
rewrite: {{.pathRewrite}}
tls: {{.useTLS}}
`


func (a *AmbassadorV1Mapper) Start(userName string, pathRewrite string, useTLS string, srv *k8sv1.Service) error {
  var tmpl *template.Template
  var err error
	if a.MappingTemplate == nil {
    tmpl, err = template.New("mapping").Parse(defaultAmbassadorYaml)
    if err != nil {
      return err
    }
  } else {
    customYaml, err := yaml.Marshal(a.MappingTemplate)
    if err != nil {
      return err
    }
    tmpl, err = template.New("mapping").Parse(string(customYaml))
    if err != nil {
      return err
    }
  }
  hostDomain := a.HostDomain
  if hostDomain == "" {
    hostDomain = "svc.cluster.local:80"
  }
  config := map[string]interface{}{
    "resourceName": srv.ObjectMeta.Name,
    "userName" : userName,
    "hostDomain" : a.HostDomain,
    "nameSpace" : srv.ObjectMeta.Namespace,
    "serviceName" : srv.ObjectMeta.Name,
    "pathRewrite" : pathRewrite,
    "useTLS" : useTLS,
  }
  buf := bytes.Buffer{}
  err = tmpl.Execute(&buf, config)
  if err != nil {
    return err
  }
	srv.ObjectMeta.Annotations["getambassador.io/config"] = string(buf.Bytes())
  return nil
}

func (a *AmbassadorV1Mapper) Stop(namespace string, name string) error {
  //nothing to do
  return nil
}
