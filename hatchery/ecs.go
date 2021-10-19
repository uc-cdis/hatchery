package hatchery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
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
	Volumes          []*ecs.Volume
	MountPoints      []*ecs.MountPoint
	LogRegion        string
	TaskRole         string
	Type             string
	EntryPoint       []string
	Args             []string
	SidecarContainer ecs.ContainerDefinition
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
	clusterName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-cluster"
	input := &ecs.CreateClusterInput{
		ClusterName: aws.String(clusterName),
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
	clusterName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-cluster"
	clusterInput := &ecs.DescribeClustersInput{
		Clusters: []*string{
			aws.String(clusterName),
		},
	}
	describeClusterResult, err := svc.DescribeClusters(clusterInput)
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
	if len(describeClusterResult.Failures) > 0 {
		for _, failure := range describeClusterResult.Failures {
			if *failure.Reason == "MISSING" {
				Config.Logger.Printf("ECS cluster named %s not found, trying to create this ECS cluster", clusterName)
				input := &ecs.CreateClusterInput{
					ClusterName: aws.String(clusterName),
				}

				_, err := svc.CreateCluster(input)
				if err != nil {
					if aerr, ok := err.(awserr.Error); ok {
						switch aerr.Code() {
						default:
							return nil, errors.New(fmt.Sprintf("Cannot create ECS cluster named %s: %s", clusterName, aerr.Code()))
						}
					}
					return nil, errors.New(fmt.Sprintf("Cannot create ECS cluster named %s: %s", clusterName, err.Error()))
				}
				Config.Logger.Printf("ECS cluster %s created for user %s", clusterName, userName)
				describeClusterResult, err = svc.DescribeClusters(clusterInput)
				if err != nil || len(describeClusterResult.Failures) > 0 {
					return nil, errors.New(fmt.Sprintf("Still cannot find ECS cluster named %s: %s", clusterName, err.Error()))
				}
				return describeClusterResult.Clusters[0], nil
			}
		}
		Config.Logger.Printf("ECS cluster named %s cannot be described", clusterName)
		return nil, errors.New(fmt.Sprintf("ECS cluster named %s cannot be described", clusterName))
	} else {
		return describeClusterResult.Clusters[0], nil
	}
}

// Status of workspace running in ECS
func (sess *CREDS) statusEcsWorkspace(ctx context.Context, userName string, accessToken string) (*WorkspaceStatus, error) {
	status := WorkspaceStatus{}
	statusMap := map[string]string{
		"ACTIVE":   "Running",
		"DRAINING": "Terminating",
		"STOPPED":  "Not Found",
		"INACTIVE": "Not Found",
	}
	statusMessage := "INACTIVE"
	status.Status = statusMap[statusMessage]
	status.IdleTimeLimit = -1
	status.LastActivityTime = -1

	cluster, err := sess.findEcsCluster(userName)
	if err != nil {
		return &status, err
	}
	service, err := sess.svc.DescribeServices(&ecs.DescribeServicesInput{
		Cluster: cluster.ClusterName,
		Services: []*string{
			aws.String(userToResourceName(userName, "pod")),
		},
	})
	if err != nil {
		return &status, err
	}

	var taskDefName string
	if len(service.Services) > 0 {
		statusMessage = *service.Services[0].Status
		if statusMessage == "ACTIVE" {
			taskDefName = *service.Services[0].TaskDefinition
			if taskDefName == "" {
				Config.Logger.Printf("No task definition found for user %s", userName)
			} else {
				desTaskDefOutput, err := sess.svc.DescribeTaskDefinition(&ecs.DescribeTaskDefinitionInput{
					TaskDefinition: &taskDefName,
				})
				if err == nil {
					containerDefs := desTaskDefOutput.TaskDefinition.ContainerDefinitions
					if len(containerDefs) > 0 {
						args := containerDefs[0].Command
						if len(args) > 0 {
							for i, arg := range args {
								if strings.Contains(*arg, "shutdown_no_activity_timeout=") {
									Config.Logger.Printf("Found kernel idle shutdown time in args. Attempting to get last activity time\n")
									argSplit := strings.Split(*arg, "=")
									idleTimeLimit, err := strconv.Atoi(argSplit[len(argSplit)-1])
									if err == nil {
										status.IdleTimeLimit = idleTimeLimit * 1000
										lastActivityTime, err := getKernelIdleTimeWithContext(ctx, accessToken)
										status.LastActivityTime = lastActivityTime
										if err != nil {
											Config.Logger.Println(err.Error())
										}
									} else {
										Config.Logger.Println(err.Error())
									}
									break
								}
								if i == len(args)-1 {
									Config.Logger.Printf("Unable to find kernel idle shutdown time in args\n")
								}
							}
						} else {
							Config.Logger.Printf("No env vars found for task definition %s\n", taskDefName)
						}
					} else {
						Config.Logger.Printf("No container definition found for task definition %s\n", taskDefName)
					}
				}
			}
		}
	} else {
		Config.Logger.Printf("No service found for user %s", userName)
	}

	status.Status = statusMap[statusMessage]
	return &status, nil
}

// Terminate workspace running in ECS
// TODO: Make this terminate ALB as well.
func terminateEcsWorkspace(ctx context.Context, userName string, accessToken string) (string, error) {
	paymodel, err := getPayModelForUser(userName)
	if err != nil {
		return "", err
	}
	roleARN := "arn:aws:iam::" + paymodel.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))
	svc := NewSession(sess, roleARN)
	cluster, err := svc.findEcsCluster(userName)
	if err != nil {
		return "", err
	}
	desServiceOutput, err := svc.svc.DescribeServices(&ecs.DescribeServicesInput{
		Cluster: cluster.ClusterName,
		Services: []*string{
			aws.String(userToResourceName(userName, "pod")),
		},
	})
	if err != nil {
		return "", err
	}
	var taskDefName string
	if len(desServiceOutput.Services) > 0 {
		taskDefName = *desServiceOutput.Services[0].TaskDefinition
	} else {
		return "", errors.New("No service found for " + userName)
	}
	if taskDefName == "" {
		Config.Logger.Printf("No task definition found for user %s, skipping API key deletion", userName)
	} else {
		desTaskDefOutput, err := svc.svc.DescribeTaskDefinition(&ecs.DescribeTaskDefinitionInput{
			TaskDefinition: &taskDefName,
		})
		if err != nil {
			return "", err
		}
		containerDefs := desTaskDefOutput.TaskDefinition.ContainerDefinitions
		if len(containerDefs) > 0 {
			envVars := containerDefs[0].Environment
			if len(envVars) > 0 {
				for i, ev := range envVars {
					if *ev.Name == "API_KEY_ID" {
						Config.Logger.Printf("Found mounted API key. Attempting to delete API Key with ID %s for user %s\n", *ev.Value, userName)
						err := deleteAPIKeyWithContext(ctx, accessToken, *ev.Value)
						if err != nil {
							Config.Logger.Printf("Error occurred when deleting API Key with ID %s for user %s: %s\n", *ev.Value, userName, err.Error())
						}
						break
					}
					if i == len(envVars)-1 {
						Config.Logger.Printf("Unable to find API Key ID in env vars for user %s\n", userName)
					}
				}
			} else {
				Config.Logger.Printf("No env vars found for task definition %s, skipping API key deletion\n", taskDefName)
			}
		} else {
			Config.Logger.Printf("No container definition found for task definition %s, skipping API key deletion\n", taskDefName)
		}
	}

	delServiceOutput, err := svc.svc.DeleteService(&ecs.DeleteServiceInput{
		Cluster: cluster.ClusterName,
		Force:   aws.Bool(true),
		Service: aws.String(userToResourceName(userName, "pod")),
	})
	if err != nil {
		return "", err
	}
	// TODO: Terminate ALB + target group here too
	return fmt.Sprintf("Service '%s' is in status: %s", userToResourceName(userName, "pod"), *delServiceOutput.Service.Status), nil
}

func launchEcsWorkspace(ctx context.Context, userName string, hash string, accessToken string) (string, error) {
	// TODO: Setup EBS volume as pd
	// Must create volume using SDK too.. :(
	paymodel, err := getPayModelForUser(userName)
	if err != nil {
		return "", err
	}
	roleARN := "arn:aws:iam::" + paymodel.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
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

	apiKey, err := getAPIKeyWithContext(ctx, accessToken)
	if err != nil {
		Config.Logger.Printf("Failed to get API key for user %v, Error: %v", userName, err)
		return "", err
	}
	Config.Logger.Printf("Created API key for user %v, key ID: %v", userName, apiKey.KeyID)

	envVars := []EnvVar{}
	for k, v := range hatchApp.Env {
		envVars = append(envVars, EnvVar{
			Key:   k,
			Value: v,
		})
	}
	envVars = append(envVars, EnvVar{
		Key:   "API_KEY",
		Value: apiKey.APIKey,
	})
	envVars = append(envVars, EnvVar{
		Key:   "API_KEY_ID",
		Value: apiKey.KeyID,
	})
	// TODO: still mounting access token for now, remove this when fully switched to use API key
	envVars = append(envVars, EnvVar{
		Key:   "ACCESS_TOKEN",
		Value: accessToken,
	})
	envVars = append(envVars, EnvVar{
		Key:   "GEN3_ENDPOINT",
		Value: os.Getenv("GEN3_ENDPOINT"),
	})
	volumes, err := svc.EFSFileSystem(userName)
	if err != nil {
		return "", err
	}

	taskRole, err := svc.taskRole(userName)
	if err != nil {
		return "", err
	}

	taskDef := CreateTaskDefinitionInput{
		Image:      hatchApp.Image,
		Cpu:        cpu,
		Memory:     mem,
		Name:       userToResourceName(userName, "pod"),
		Type:       "ws",
		TaskRole:   *taskRole,
		EntryPoint: hatchApp.Command,
		Volumes: []*ecs.Volume{
			{
				Name: aws.String("pd"),
				EfsVolumeConfiguration: &ecs.EFSVolumeConfiguration{
					AuthorizationConfig: &ecs.EFSAuthorizationConfig{
						AccessPointId: &volumes.AccessPointId,
						Iam:           aws.String("ENABLED"),
					},
					FileSystemId:      &volumes.FileSystemId,
					RootDirectory:     aws.String("/"),
					TransitEncryption: aws.String("ENABLED"),
				},
			},
			{
				Name: aws.String("data-volume"),
			},
			{
				Name: aws.String("gen3"),
			},
		},
		MountPoints: []*ecs.MountPoint{
			// TODO: make these path respect the container def in hatchery config
			{
				ContainerPath: aws.String("/home/jovyan/data"),
				SourceVolume:  aws.String("data-volume"),
			},
			{
				ContainerPath: aws.String("/home/jovyan/pd"),
				SourceVolume:  aws.String("pd"),
			},
			{
				ContainerPath: aws.String("/home/jovyan/.gen3"),
				SourceVolume:  aws.String("gen3"),
			},
		},
		Args:             hatchApp.Args,
		EnvVars:          envVars,
		Port:             int64(hatchApp.TargetPort),
		ExecutionRoleArn: fmt.Sprintf("arn:aws:iam::%s:role/ecsTaskExecutionRole", paymodel.AWSAccountId), // TODO: Make this configurable?
		SidecarContainer: ecs.ContainerDefinition{
			Image: &Config.Config.Sidecar.Image,
			Name:  aws.String("sidecar-container"),
			// 2 seconds is the smallest value allowed.
			StopTimeout: aws.Int64(2),
			Essential:   aws.Bool(false),
			MountPoints: []*ecs.MountPoint{
				{
					ContainerPath: aws.String("/data"),
					SourceVolume:  aws.String("data-volume"),
				},
				{
					ContainerPath: aws.String("/.gen3"),
					SourceVolume:  aws.String("gen3"),
				},
			},
		},
	}
	taskDefResult, err := svc.CreateTaskDefinition(&taskDef, userName, hash)
	if err != nil {
		deleteAPIKeyWithContext(ctx, accessToken, apiKey.KeyID)
		return "", err
	}

	launchTask, err := svc.launchService(ctx, taskDefResult, userName, hash)
	if err != nil {
		deleteAPIKeyWithContext(ctx, accessToken, apiKey.KeyID)
		return "", err
	}

	return launchTask, nil
}

// Launch ECS service for task definition + LB for routing
func (sess *CREDS) launchService(ctx context.Context, taskDefArn string, userName string, hash string) (string, error) {
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
	err = createLocalService(ctx, userName, hash, *loadBalancer.LoadBalancers[0].DNSName, true)
	if err != nil {
		return "", err
	}
	return *loadBalancer.LoadBalancers[0].DNSName, nil
}

// Create/Update Task Definition in ECS
func (sess *CREDS) CreateTaskDefinition(input *CreateTaskDefinitionInput, userName string, hash string) (string, error) {
	creds := sess.creds
	paymodel, err := getPayModelForUser(userName)
	if err != nil {
		return "", err
	}
	LogGroup, err := sess.CreateLogGroup(fmt.Sprintf("/hatchery/%s/", paymodel.AWSAccountId), creds)
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
		StopTimeout:      aws.Int64(2),
		Essential:        aws.Bool(true),
		MountPoints:      input.MountPoints,
		Image:            aws.String(input.Image),
		LogConfiguration: logConfiguration,
		Name:             aws.String(input.Name),
		EntryPoint:       aws.StringSlice(input.EntryPoint),
		Command:          aws.StringSlice(input.Args),
	}

	sidecarContainerDefinition := input.SidecarContainer
	sidecarContainerDefinition.LogConfiguration = logConfiguration
	sidecarContainerDefinition.Environment = input.Environment()

	if input.Port != 0 {
		containerDefinition.SetPortMappings(
			[]*ecs.PortMapping{
				{
					ContainerPort: aws.Int64(int64(input.Port)),
				},
			},
		)
	}

	resp, err := svc.RegisterTaskDefinition(
		&ecs.RegisterTaskDefinitionInput{
			ContainerDefinitions: []*ecs.ContainerDefinition{
				containerDefinition,
				&sidecarContainerDefinition,
			},
			Cpu:                     aws.String(input.Cpu),
			ExecutionRoleArn:        aws.String(input.ExecutionRoleArn),
			Family:                  aws.String(fmt.Sprintf("%s_%s", input.Type, input.Name)),
			Memory:                  aws.String(input.Memory),
			NetworkMode:             aws.String(ecs.NetworkModeAwsvpc),
			RequiresCompatibilities: aws.StringSlice([]string{ecs.CompatibilityFargate}),
			TaskRoleArn:             aws.String(input.TaskRole),
			Volumes:                 input.Volumes,
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
