module github.com/uc-cdis/hatchery

go 1.14

require (
	github.com/aws/aws-sdk-go v1.38.65
	github.com/go-logr/logr v0.2.1 // indirect
	github.com/golang/protobuf v1.4.3 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/googleapis/gnostic v0.5.2 // indirect
	golang.org/x/crypto v0.0.0-20201016220609-9e8e0b390897 // indirect
	golang.org/x/oauth2 v0.0.0-20200902213428-5d25da1a8d43 // indirect
	golang.org/x/sys v0.0.0-20201018230417-eeed37f84f13 // indirect
	golang.org/x/time v0.0.0-20200630173020-3af7569d3a1e // indirect
	google.golang.org/appengine v1.6.7 // indirect
	gopkg.in/yaml.v2 v2.3.0
	k8s.io/api v0.19.3
	k8s.io/apimachinery v0.19.3
	k8s.io/client-go v1.5.1
	k8s.io/klog/v2 v2.3.0 // indirect
	k8s.io/utils v0.0.0-20201015054608-420da100c033 // indirect
	sigs.k8s.io/aws-iam-authenticator v0.5.3
)

replace (
	// lateest gnostic has some API changes that break things
	github.com/googleapis/gnostic => github.com/googleapis/gnostic v0.5.2
	// versioning is screwed up in the clinet-go module
	k8s.io/client-go => k8s.io/client-go v0.19.2
)
