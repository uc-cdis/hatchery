package hatchery

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type CREDS struct {
	svc   *ecs.Client
	creds *aws.CredentialsCache
}

// TODO all reference to this function will need to be updated
// Currently esc.go is updated
func NewSVC(cfg aws.Config, roleArn string) CREDS {
	stsClient := sts.NewFromConfig(cfg)

	assumeRoleProvider := stscreds.NewAssumeRoleProvider(stsClient, roleArn)
	creds := aws.NewCredentialsCache(assumeRoleProvider)

	assumedCfg := cfg
	assumedCfg.Credentials = creds
	//TODO: let the config decide which region?
	//Calling reference to NewSVC all define the region as well
	//so region is currently being set twice
	assumedCfg.Region = "us-east-1"

	return CREDS{
		creds: creds,
		svc:   ecs.NewFromConfig(assumedCfg),
	}
}
