package hatchery

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatchlogs"
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
	EntryPoint       []string
	Args             []string
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

// Create ECS cluster
func (sess *CREDS) launchEcsCluster(userName string) (*ecs.Cluster, error) {
	svc := sess.svc
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

func (sess *CREDS) findEcsCluster(userName string) ([]*ecs.Cluster, error) {
	svc := sess.svc
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

func StrToInt(str string) (string, error) {
	nonFractionalPart := strings.Split(str, ".")
	return nonFractionalPart[0], nil
}

func mem(str string) (string, error) {
	res := regexp.MustCompile(`(\d*)([M|G])ib?`)
	matches := res.FindStringSubmatch(str)
	num, err := strconv.Atoi(matches[1])
	if err != nil {
		return "", err
	}
	if matches[2] == "G" {
		num = num * 1024
	}
	return strconv.Itoa(num), nil
}

func cpu(str string) (string, error) {
	num, err := strconv.Atoi(str[:strings.IndexByte(str, '.')])
	if err != nil {
		return "", err
	}
	num = num * 1024
	return strconv.Itoa(num), nil
}

func launchEcsWorkspace(userName string, hash string, accessToken string) (string, error) {
	pm := Config.PayModelMap[userName]
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"

	svc := NewSession(sess, roleARN)
	Config.Logger.Printf("%s", userName)
	hatchApp := Config.ContainersMap[hash]
	mem, err := mem(hatchApp.MemoryLimit)
	if err != nil {
		return "", err
	}
	cpu, err := cpu(hatchApp.CPULimit)
	if err != nil {
		return "", err
	}
	e := []EnvVar{}
	for k, v := range Config.Config.Sidecar.Env {
		e = append(e, EnvVar{
			Key:   k,
			Value: v,
		})
	}
	taskDef := CreateTaskDefinitionInput{
		Image:            hatchApp.Image, // TODO: test all images. Tested with smaller image "jupyter/minimal-notebook:latest",
		Cpu:              cpu,
		Memory:           mem,
		Name:             hash,
		EntryPoint:       hatchApp.Command,
		Args:             hatchApp.Args,
		EnvVars:          e,
		Port:             int64(hatchApp.TargetPort),
		ExecutionRoleArn: fmt.Sprintf("arn:aws:iam::%s:role/ecsTaskExecutionRole", Config.PayModelMap[userName].AWSAccountId), // TODO: Make this configurable?
	}
	taskDefResult, err := svc.CreateTaskDefinition(&taskDef, userName, hash)
	if err != nil {
		return "", err // TODO: Make this better? clearer?
	}

	launchTask, err := svc.launchService(taskDefResult, accessToken)
	return launchTask, nil
}

// Launch the workspace container + LB for routing
func (sess *CREDS) launchService(taskDefArn string, accessToken string) (string, error) {
	// svc := ecs.New(session.New())
	// input := &ecs.CreateServiceInput{
	// 	DesiredCount:   aws.Int64(1),
	// 	ServiceName:    aws.String("ecs-simple-service"),
	// 	TaskDefinition: &taskDefArn,
	// }

	// result, err := svc.CreateService(input)
	// if err != nil {
	// 	if aerr, ok := err.(awserr.Error); ok {
	// 		switch aerr.Code() {
	// 		case ecs.ErrCodeServerException:
	// 			fmt.Println(ecs.ErrCodeServerException, aerr.Error())
	// 		case ecs.ErrCodeClientException:
	// 			fmt.Println(ecs.ErrCodeClientException, aerr.Error())
	// 		case ecs.ErrCodeInvalidParameterException:
	// 			fmt.Println(ecs.ErrCodeInvalidParameterException, aerr.Error())
	// 		case ecs.ErrCodeClusterNotFoundException:
	// 			fmt.Println(ecs.ErrCodeClusterNotFoundException, aerr.Error())
	// 		case ecs.ErrCodeUnsupportedFeatureException:
	// 			fmt.Println(ecs.ErrCodeUnsupportedFeatureException, aerr.Error())
	// 		case ecs.ErrCodePlatformUnknownException:
	// 			fmt.Println(ecs.ErrCodePlatformUnknownException, aerr.Error())
	// 		case ecs.ErrCodePlatformTaskDefinitionIncompatibilityException:
	// 			fmt.Println(ecs.ErrCodePlatformTaskDefinitionIncompatibilityException, aerr.Error())
	// 		case ecs.ErrCodeAccessDeniedException:
	// 			fmt.Println(ecs.ErrCodeAccessDeniedException, aerr.Error())
	// 		default:
	// 			fmt.Println(aerr.Error())
	// 		}
	// 	} else {
	// 		// Print the error, cast err to awserr.Error to get the Code and
	// 		// Message from an error.
	// 		fmt.Println(err.Error())
	// 	}
	// }

	// fmt.Println(result)

	return taskDefArn, nil

}

// Create/Update Task Definition in ECS
func (sess *CREDS) CreateTaskDefinition(input *CreateTaskDefinitionInput, userName string, hash string) (string, error) {
	creds := sess.creds
	LogGroup, err := sess.CreateLogGroup(fmt.Sprintf("/hatchery/%s/", Config.PayModelMap[userName].AWSAccountId), creds)
	if err != nil {
		Config.Logger.Printf("Failed to create/get LogGroup. Error: %s", err)
		return "", err
	}
	svc := ecs.New(session.New(&aws.Config{
		Credentials: creds,
		Region:      aws.String("us-east-1"),
	}))

	Config.Logger.Printf("Creating ECS task definition")

	logConfiguration := &ecs.LogConfiguration{
		LogDriver: aws.String(ecs.LogDriverAwslogs),
		Options: map[string]*string{
			"awslogs-region":        aws.String("us-east-1"),
			"awslogs-group":         aws.String(LogGroup),
			"awslogs-stream-prefix": aws.String(userName),
		},
	}

	containerDefinition := &ecs.ContainerDefinition{
		Environment:      input.Environment(),
		Essential:        aws.Bool(true),
		Image:            aws.String(input.Image),
		LogConfiguration: logConfiguration,
		Name:             aws.String(input.Name),
		EntryPoint:       aws.StringSlice(input.EntryPoint),
		Command:          aws.StringSlice(input.Args),
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
		Config.Logger.Print(err, " Couldn't register ECS task definition")
		return "", err
	}

	td := resp.TaskDefinition

	Config.Logger.Printf("Created ECS task definition [%s:%d]", aws.StringValue(td.Family), aws.Int64Value(td.Revision))

	return aws.StringValue(td.TaskDefinitionArn), nil
}

// func DescribeTaskDefinition(svc *ecs.ECS, hash string) (*ecs.DescribeTaskDefinitionOutput, error) {
// 	describeTaskDefinitionInput := ecs.DescribeTaskDefinitionInput{
// 		TaskDefinition: &hash,
// 	}
// 	taskDef, err := svc.DescribeTaskDefinition(&describeTaskDefinitionInput)
// 	if err != nil {
// 		Config.Logger.Printf("taskdefDescribe error: %s", err)
// 		return nil, err
// 	}
// 	return taskDef, nil
// }

//Create CloudWatch LogGroup for hatchery containers
func (sess *CREDS) CreateLogGroup(LogGroupName string, creds *credentials.Credentials) (string, error) {
	c := cloudwatchlogs.New(session.New(&aws.Config{
		Credentials: creds,
		Region:      aws.String("us-east-1"),
	}))

	describeLogGroupIn := &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(LogGroupName),
	}

	logGroup, err := c.DescribeLogGroups(describeLogGroupIn)
	if err != nil {
		Config.Logger.Printf("Error in DescribeLogGroup: %s", err)
		return "", err
	}
	if len(logGroup.LogGroups) < 0 {
		createLogGroupIn := &cloudwatchlogs.CreateLogGroupInput{
			LogGroupName: aws.String(LogGroupName),
		}
		newLogGroup, err := c.CreateLogGroup(createLogGroupIn)
		if err != nil {
			Config.Logger.Printf("Error in  CreateLogGroup: %s, %s", err, newLogGroup)
			return "", err
		}
		return newLogGroup.String(), nil
	}
	return *logGroup.LogGroups[0].LogGroupName, nil
}
