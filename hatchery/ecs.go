package hatchery

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
)

type CreateTaskDefinitionInput struct {
	Cpu              string
	EnvVars          []EnvVar
	ExecutionRoleArn string
	Image            string
	Memory           string
	Name             string
	Port             int64
	LogGroupName     string
	LogRegion        string
	TaskRole         string
	Type             string
}

type EnvVar struct {
	Key   string
	Value string
}

func (input *CreateTaskDefinitionInput) Environment() []*ecs.KeyValuePair {
	var environment []*ecs.KeyValuePair

	for _, envVar := range input.EnvVars {
		environment = append(environment,
			&ecs.KeyValuePair{
				Name:  aws.String(envVar.Key),
				Value: aws.String(envVar.Value),
			},
		)
	}

	return environment
}

const logStreamPrefix = "fargate"

// To create a new cluster
//
// This example creates a cluster in your default region.
func launchEcsCluster(userName string) (*ecs.Cluster, error) {
	pm := Config.PayModelMap[userName]
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String(pm.Region),
	}))

	creds := stscreds.NewCredentials(sess, roleARN)

	svc := ecs.New(session.New(&aws.Config{
		Credentials: creds,
		Region:      aws.String("us-east-1"),
	}))
	cluster_name := strings.ReplaceAll(Config.Config.Sidecar.Env["HOSTNAME"], ".", "-") + "-cluster"
	input := &ecs.CreateClusterInput{
		ClusterName: aws.String(cluster_name),
	}

	result, err := svc.CreateCluster(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			default:
				return nil, aerr
			}
		}
		return nil, err
	}
	return result.Cluster, nil
}

func findEcsCluster(userName string) ([]*ecs.Cluster, error) {
	pm := Config.PayModelMap[userName]
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String(pm.Region),
	}))

	creds := stscreds.NewCredentials(sess, roleARN)

	svc := ecs.New(session.New(&aws.Config{
		Credentials: creds,
		Region:      aws.String("us-east-1"),
	}))
	cluster_name := strings.ReplaceAll(Config.Config.Sidecar.Env["HOSTNAME"], ".", "-") + "-cluster"
	cluster_input := &ecs.DescribeClustersInput{
		Clusters: []*string{
			aws.String(cluster_name),
		},
	}
	result, err := svc.DescribeClusters(cluster_input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			default:
				return nil, aerr
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			fmt.Println(err.Error())
		}
	}
	if len(result.Failures) > 0 {
		Config.Logger.Printf("ECS cluster named %s not found", cluster_name)
		return nil, errors.New(fmt.Sprintf("ECS cluster named %s not found", cluster_name))
	} else {
		return result.Clusters, nil
	}
}

func launchEcsWorkspace(userName string, hash string) (string, error) {
	Config.Logger.Printf("%s", userName)
	return userName, nil

}

func DescribeTaskDefinition(svc *ecs.ECS, hash string) (*ecs.DescribeTaskDefinitionOutput, error) {
	describeTaskDefinitionInput := ecs.DescribeTaskDefinitionInput{
		TaskDefinition: &hash,
	}
	taskDef, err := svc.DescribeTaskDefinition(&describeTaskDefinitionInput)
	if err != nil {
		return nil, err
	}
	return taskDef, nil
}

// create Task Definitioin in ECS
func CreateTaskDefinition(input *CreateTaskDefinitionInput, userName string, hash string) string {
	pm := Config.PayModelMap[userName]
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String(pm.Region),
	}))
	Config.Logger.Printf("Assuming role: %s", roleARN)

	creds := stscreds.NewCredentials(sess, roleARN)

	svc := ecs.New(session.New(&aws.Config{
		Credentials: creds,
		Region:      aws.String("us-east-1"),
	}))

	Config.Logger.Printf("Checking if ECS task definition exists")
	taskDef, _ := DescribeTaskDefinition(svc, hash)

	Config.Logger.Printf("TaskDef: %s", taskDef)
	Config.Logger.Printf("Creating ECS task definition")

	logConfiguration := &ecs.LogConfiguration{
		LogDriver: aws.String(ecs.LogDriverAwslogs),
		Options: map[string]*string{
			"awslogs-region":        aws.String(input.LogRegion),
			"awslogs-group":         aws.String(input.LogGroupName),
			"awslogs-stream-prefix": aws.String(logStreamPrefix),
		},
	}

	containerDefinition := &ecs.ContainerDefinition{
		Environment:      input.Environment(),
		Essential:        aws.Bool(true),
		Image:            aws.String(input.Image),
		LogConfiguration: logConfiguration,
		Name:             aws.String(input.Name),
	}

	if input.Port != 0 {
		containerDefinition.SetPortMappings(
			[]*ecs.PortMapping{
				&ecs.PortMapping{
					ContainerPort: aws.Int64(int64(input.Port)),
				},
			},
		)
	}

	resp, err := svc.RegisterTaskDefinition(
		&ecs.RegisterTaskDefinitionInput{
			ContainerDefinitions:    []*ecs.ContainerDefinition{containerDefinition},
			Cpu:                     aws.String(input.Cpu),
			ExecutionRoleArn:        aws.String(input.ExecutionRoleArn),
			Family:                  aws.String(fmt.Sprintf("%s_%s", input.Type, input.Name)),
			Memory:                  aws.String(input.Memory),
			NetworkMode:             aws.String(ecs.NetworkModeAwsvpc),
			RequiresCompatibilities: aws.StringSlice([]string{ecs.CompatibilityFargate}),
			TaskRoleArn:             aws.String(input.TaskRole),
		},
	)

	if err != nil {
		Config.Logger.Print(err, "Couldn't register ECS task definition")
	}

	td := resp.TaskDefinition

	Config.Logger.Printf("Created ECS task definition [%s:%d]", aws.StringValue(td.Family), aws.Int64Value(td.Revision))

	return aws.StringValue(td.TaskDefinitionArn)
}
