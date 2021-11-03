package hatchery

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
)

type CREDS struct {
	svc   *ecs.ECS
	creds *credentials.Credentials
}

func NewSession(sess *session.Session, roleArn string) CREDS {
	creds := stscreds.NewCredentials(sess, roleArn)
	return CREDS{
		creds: creds,
		svc: ecs.New(session.New(&aws.Config{
			Credentials: creds,
			Region:      aws.String("us-east-1"),
		})),
	}
}
