package hatchery

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
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
// TODO: Evaluate if this is still this needed..
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

func (sess *CREDS) findEcsCluster(userName string) (*ecs.Cluster, error) {
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
			Config.Logger.Println(err.Error())
		}
	}
	if len(result.Failures) > 0 {
		Config.Logger.Printf("ECS cluster named %s not found", cluster_name)
		return nil, errors.New(fmt.Sprintf("ECS cluster named %s not found", cluster_name))
	} else {
		return result.Clusters[0], nil
	}
}

// Status of workspace running in ECS
func (sess *CREDS) statusEcsWorkspace(userName string) (*WorkspaceStatus, error) {
	status := WorkspaceStatus{}
	cluster, err := sess.findEcsCluster(userName)
	if err != nil {
		return nil, err
	}
	service, err := sess.svc.DescribeServices(&ecs.DescribeServicesInput{
		Cluster: cluster.ClusterName,
		Services: []*string{
			aws.String(userToResourceName(userName, "pod")),
		},
	})

	statusMessage := "INACTIVE"
	if len(service.Services) > 0 {
		statusMessage = *service.Services[0].Status
	}

	if err != nil {
		return nil, err
	}
	statusMap := map[string]string{
		"ACTIVE":   "Running",
		"DRAINING": "Terminating",
		"STOPPED":  "Not Found",
		"INACTIVE": "Not Found",
	}

	status.Status = statusMap[statusMessage]
	return &status, nil
}

// Terminate workspace running in ECS
// TODO: Make this terminate ALB as well.
func terminateEcsWorkspace(userName string) (string, error) {
	pm := Config.PayModelMap[userName]
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String("us-east-1"),
	}))
	svc := NewSession(sess, roleARN)
	cluster, err := svc.findEcsCluster(userName)
	if err != nil {
		return "", err
	}
	service, err := svc.svc.DeleteService(&ecs.DeleteServiceInput{
		Cluster: cluster.ClusterName,
		Force:   aws.Bool(true),
		Service: aws.String(userToResourceName(userName, "pod")),
	})
	if err != nil {
		return "", err
	}
	// TODO: Terminate ALB + target group here too
	return fmt.Sprintf("Service '%s' is in status: %s", userToResourceName(userName, "pod"), *service.Service.Status), nil
}

func launchEcsWorkspace(userName string, hash string, accessToken string) (string, error) {
	// TODO: Setup EBS volume as pd
	// Must create volume using SDK too.. :(
	pm := Config.PayModelMap[userName]
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String("us-east-1"),
	}))
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
	e = append(e, EnvVar{
		Key:   "ACCESS_TOKEN",
		Value: accessToken,
	})
	taskDef := CreateTaskDefinitionInput{
		Image:            hatchApp.Image,
		Cpu:              cpu,
		Memory:           mem,
		Name:             userToResourceName(userName, "pod"),
		Type:             "testing-ws",
		EntryPoint:       hatchApp.Command,
		Args:             hatchApp.Args,
		EnvVars:          e,
		Port:             int64(hatchApp.TargetPort),
		ExecutionRoleArn: fmt.Sprintf("arn:aws:iam::%s:role/ecsTaskExecutionRole", Config.PayModelMap[userName].AWSAccountId), // TODO: Make this configurable?
	}
	taskDefResult, err := svc.CreateTaskDefinition(&taskDef, userName, hash)
	if err != nil {
		return "", err
	}

	launchTask, err := svc.launchService(taskDefResult, userName, hash)
	if err != nil {
		return "", err
	}

	return launchTask, nil
}

// Launch ECS service for task definition + LB for routing
func (sess *CREDS) launchService(taskDefArn string, userName string, hash string) (string, error) {
	svc := sess.svc
	hatchApp := Config.ContainersMap[hash]
	cluster, err := sess.findEcsCluster(userName)
	if err != nil {
		return "", err
	}
	Config.Logger.Printf("Cluster: %s", *cluster.ClusterName)

	networkConfig, _ := sess.networkConfig()

	loadBalancer, targetGroupArn, _, err := sess.CreateLoadBalancer(userName)
	if err != nil {
		return "", err
	}

	input := &ecs.CreateServiceInput{
		DesiredCount:         aws.Int64(1),
		Cluster:              cluster.ClusterArn,
		ServiceName:          aws.String(userToResourceName(userName, "pod")),
		TaskDefinition:       &taskDefArn,
		NetworkConfiguration: &networkConfig,
		DeploymentConfiguration: &ecs.DeploymentConfiguration{
			MinimumHealthyPercent: aws.Int64(0),
		},
		EnableECSManagedTags: aws.Bool(true),
		LaunchType:           aws.String("FARGATE"),
		LoadBalancers: []*ecs.LoadBalancer{
			{
				ContainerName:  aws.String(userToResourceName(userName, "pod")),
				ContainerPort:  aws.Int64(int64(hatchApp.TargetPort)),
				TargetGroupArn: targetGroupArn,
			},
		},
	}

	result, err := svc.CreateService(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case ecs.ErrCodeServerException:
				Config.Logger.Println(ecs.ErrCodeServerException, aerr.Error())
			case ecs.ErrCodeClientException:
				Config.Logger.Println(ecs.ErrCodeClientException, aerr.Error())
			case ecs.ErrCodeInvalidParameterException:
				Config.Logger.Println(ecs.ErrCodeInvalidParameterException, aerr.Error())
			case ecs.ErrCodeClusterNotFoundException:
				Config.Logger.Println(ecs.ErrCodeClusterNotFoundException, aerr.Error())
			case ecs.ErrCodeUnsupportedFeatureException:
				Config.Logger.Println(ecs.ErrCodeUnsupportedFeatureException, aerr.Error())
			case ecs.ErrCodePlatformUnknownException:
				Config.Logger.Println(ecs.ErrCodePlatformUnknownException, aerr.Error())
			case ecs.ErrCodePlatformTaskDefinitionIncompatibilityException:
				Config.Logger.Println(ecs.ErrCodePlatformTaskDefinitionIncompatibilityException, aerr.Error())
			case ecs.ErrCodeAccessDeniedException:
				Config.Logger.Println(ecs.ErrCodeAccessDeniedException, aerr.Error())
			default:
				Config.Logger.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			Config.Logger.Println(err.Error())
		}
		return "", err
	}
	Config.Logger.Printf("Service launched: %s", *result.Service.ClusterArn)
	err = createLocalService(userName, hash, *loadBalancer.LoadBalancers[0].DNSName, int32(80))
	if err != nil {
		return "", err
	}
	return *loadBalancer.LoadBalancers[0].DNSName, nil
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
