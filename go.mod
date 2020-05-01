module github.com/uc-cdis/hatchery

go 1.13

require (
	cloud.google.com/go v0.38.0 // indirect
	github.com/Azure/go-autorest/autorest v0.9.0 // indirect
	github.com/docker/spdystream v0.0.0-20160310174837-449fdfce4d96 // indirect
	github.com/evanphx/json-patch v4.2.0+incompatible // indirect
	github.com/golang/groupcache v0.0.0-20160516000752-02826c3e7903 // indirect
	github.com/golang/protobuf v1.4.0 // indirect
	github.com/google/btree v1.0.0 // indirect
	github.com/google/uuid v1.1.1 // indirect
	github.com/googleapis/gnostic v0.4.1 // indirect
	github.com/gophercloud/gophercloud v0.1.0 // indirect
	github.com/gregjones/httpcache v0.0.0-20180305231024-9cad4c3443a7 // indirect
	github.com/hashicorp/golang-lru v0.5.1 // indirect
	github.com/imdario/mergo v0.3.5 // indirect
	github.com/json-iterator/go v1.1.9 // indirect
	github.com/modern-go/reflect2 v1.0.1 // indirect
	github.com/mxk/go-flowrate v0.0.0-20140419014527-cca7078d478f // indirect
	github.com/onsi/gomega v1.7.0 // indirect
	github.com/peterbourgon/diskv v2.0.1+incompatible // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/stretchr/testify v1.4.0 // indirect
	golang.org/x/crypto v0.0.0-20200427165652-729f1e841bcc // indirect
	golang.org/x/net v0.0.0-20200425230154-ff2c4b7c35a0 // indirect
	golang.org/x/oauth2 v0.0.0-20200107190931-bf48bf16ab8d // indirect
	golang.org/x/sys v0.0.0-20200428200454-593003d681fa // indirect
	golang.org/x/text v0.3.2 // indirect
	golang.org/x/time v0.0.0-20200416051211-89c76fbcd5d1 // indirect
	google.golang.org/appengine v1.6.6 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v2 v2.2.8
	k8s.io/api v0.18.2
	k8s.io/apimachinery v0.18.2
	k8s.io/client-go v0.18.2
	k8s.io/klog v1.0.0 // indirect
	k8s.io/utils v0.0.0-20200414100711-2df71ebbae66 // indirect
	sigs.k8s.io/structured-merge-diff v0.0.0-20190525122527-15d366b2352e // indirect
)

replace (
  // lateest gnostic has some API changes that break things
	github.com/googleapis/gnostic => github.com/googleapis/gnostic v0.3.1
	// versioning is screwed up in the clinet-go module
  k8s.io/client-go => k8s.io/client-go v0.18.2
)
