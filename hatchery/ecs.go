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
func (sess *CREDS) launchEcsCluster(userName string) (*ecs.Cluster, error) {
	svc := sess.svc
	clusterName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-cluster"

	// Setting up remote VPC
	_, err := setupVPC(userName)
	if err != nil {
		return nil, err
	}

	describeClusterInput := &ecs.DescribeClustersInput{
		Clusters: []*string{aws.String(clusterName)},
	}

	exCluster, err := svc.DescribeClusters(describeClusterInput)
	if err != nil {
		return nil, err
	}
	provision := false
	if len(exCluster.Clusters) == 1 {
		if *exCluster.Clusters[0].Status == "INACTIVE" {
			// Force recreation of inactive/deleted clusters
			provision = true
		}
	}
	if len(exCluster.Clusters) == 0 || provision {

		input := &ecs.CreateClusterInput{
			ClusterName: aws.String(clusterName),
			Tags: []*ecs.Tag{
				{
					Key:   aws.String("Name"),
					Value: aws.String(clusterName),
				},
				{
					Key:   aws.String("Environment"),
					Value: aws.String(os.Getenv("GEN3_ENDPOINT")),
				},
			},
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
	return exCluster.Clusters[0], nil
}

func (sess *CREDS) findEcsCluster() (*ecs.Cluster, error) {
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
					Tags: []*ecs.Tag{
						{
							Key:   aws.String("Name"),
							Value: aws.String(clusterName),
						},
						{
							Key:   aws.String("Environment"),
							Value: aws.String(os.Getenv("GEN3_ENDPOINT")),
						},
					},
				}

				_, err := svc.CreateCluster(input)
				if err != nil {
					if aerr, ok := err.(awserr.Error); ok {
						switch aerr.Code() {
						default:
							return nil, fmt.Errorf("Cannot create ECS cluster named %s: %s", clusterName, aerr.Code())
						}
					}
					return nil, fmt.Errorf("Cannot create ECS cluster named %s: %s", clusterName, err.Error())
				}
				describeClusterResult, err = svc.DescribeClusters(clusterInput)
				if err != nil || len(describeClusterResult.Failures) > 0 {
					return nil, fmt.Errorf("Still cannot find ECS cluster named %s: %s", clusterName, err.Error())
				}
				return describeClusterResult.Clusters[0], nil
			}
		}
		Config.Logger.Printf("ECS cluster named %s cannot be described", clusterName)
		return nil, fmt.Errorf("ECS cluster named %s cannot be described", clusterName)
	} else {
		return describeClusterResult.Clusters[0], nil
	}
}

// Status of workspace running in ECS
func (sess *CREDS) statusEcsWorkspace(ctx context.Context, userName string, accessToken string) (*WorkspaceStatus, error) {
	status := WorkspaceStatus{}
	statusMap := map[string]string{
		"ACTIVE":    "Running",
		"DRAINING":  "Terminating",
		"LAUNCHING": "Launching",
		"STOPPED":   "Not Found",
		"INACTIVE":  "Not Found",
	}
	statusMessage := "INACTIVE"
	status.Status = statusMap[statusMessage]
	status.IdleTimeLimit = -1
	status.LastActivityTime = -1
	svcName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + userToResourceName(userName, "pod") + "svc"
	cluster, err := sess.findEcsCluster()
	if err != nil {
		return &status, err
	}
	service, err := sess.svc.DescribeServices(&ecs.DescribeServicesInput{
		Cluster: cluster.ClusterName,
		Services: []*string{
			aws.String(svcName),
		},
	})
	if err != nil {
		return &status, err
	}
	// TODO: Check TransitGatewayAttachment is not in Deleting state (Can't create new one until it's deleted).
	var taskDefName string
	if len(service.Services) > 0 {
		statusMessage = *service.Services[0].Status
		if statusMessage == "ACTIVE" && (*service.Services[0].RunningCount == *service.Services[0].DesiredCount) {
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
		if (*service.Services[0].PendingCount > *service.Services[0].RunningCount) || *service.Services[0].PendingCount > 0 {
			status.Status = statusMap["LAUNCHING"]
		} else {
			status.Status = statusMap[statusMessage]
		}
	} else {
		status.Status = statusMap[statusMessage]
	}
	return &status, nil
}

// Terminate workspace running in ECS
// TODO: Make this terminate ALB as well.
func terminateEcsWorkspace(ctx context.Context, userName string, accessToken string, awsAcctID string) (string, error) {
	Config.Logger.Printf("Terminating ECS workspace for user %s", userName)
	roleARN := "arn:aws:iam::" + awsAcctID + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))
	svc := NewSVC(sess, roleARN)
	cluster, err := svc.findEcsCluster()
	if err != nil {
		return "", err
	}
	svcName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + userToResourceName(userName, "pod") + "svc"
	desServiceOutput, err := svc.svc.DescribeServices(&ecs.DescribeServicesInput{
		Cluster: cluster.ClusterName,
		Services: []*string{
			aws.String(svcName),
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
	// Terminate ECS service
	Config.Logger.Printf("Terminating ECS service %s for user %s\n", svcName, userName)
	delServiceOutput, err := svc.svc.DeleteService(&ecs.DeleteServiceInput{
		Cluster: cluster.ClusterName,
		Force:   aws.Bool(true),
		Service: aws.String(svcName),
	})
	if err != nil {
		return "", err
	}

	// Terminate load balancer
	Config.Logger.Printf("Terminating load balancer for user %s\n", userName)
	err = svc.terminateLoadBalancer(userName)
	if err != nil {
		Config.Logger.Printf("Error occurred when terminating load balancer for user %s: %s\n", userName, err.Error())
	}

	// Terminate target group
	svc.terminateLoadBalancerTargetGroup(userName)

	// Terminate transit gateway
	Config.Logger.Printf("Terminating transit gateway for user %s\n", userName)
	err = teardownTransitGateway(userName)
	if err != nil {
		Config.Logger.Printf("Error occurred when terminating transit gateway for user %s: %s\n", userName, err.Error())
	}
	return fmt.Sprintf("Service '%s' is in status: %s", userToResourceName(userName, "pod"), *delServiceOutput.Service.Status), nil
}

func launchEcsWorkspace(ctx context.Context, userName string, hash string, accessToken string, payModel PayModel) error {
	roleARN := "arn:aws:iam::" + payModel.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))
	svc := NewSVC(sess, roleARN)
	hatchApp := Config.ContainersMap[hash]
	mem, err := mem(hatchApp.MemoryLimit)
	if err != nil {
		return err
	}
	cpu, err := cpu(hatchApp.CPULimit)
	if err != nil {
		return err
	}

	// Launch ECS cluster
	_, err = svc.launchEcsCluster(userName)
	if err != nil {
		return err
	}

	// Get API key
	Config.Logger.Printf("Creating API key for user %s", userName)
	apiKey, err := getAPIKeyWithContext(ctx, accessToken)
	if err != nil {
		Config.Logger.Printf("Failed to create API key for user %v, Error: %v. Moving on but workspace won't have API key", userName, err)
		apiKey = &APIKeyStruct{}
	} else {
		Config.Logger.Printf("Created API key for user %v, key ID: %v", userName, apiKey.KeyID)
	}

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

	Config.Logger.Printf("Settign up EFS for user %s", userName)
	volumes, err := svc.EFSFileSystem(userName)
	if err != nil {
		return err
	}

	Config.Logger.Printf("Setting up task role for user %s", userName)
	taskRole, err := svc.taskRole(userName)
	if err != nil {
		return err
	}

	Config.Logger.Printf("Setting up execution role for user %s", userName)
	_, err = svc.CreateEcsTaskExecutionRole()
	if err != nil {
		return err
	}

	Config.Logger.Printf("Setting up ECS task definition for user %s", userName)
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
				ReadOnly:      aws.Bool(false),
			},
			{
				ContainerPath: aws.String("/home/jovyan/pd"),
				SourceVolume:  aws.String("pd"),
				ReadOnly:      aws.Bool(false),
			},
			{
				ContainerPath: aws.String("/home/jovyan/.gen3"),
				SourceVolume:  aws.String("gen3"),
				ReadOnly:      aws.Bool(false),
			},
		},
		Args:             hatchApp.Args,
		EnvVars:          envVars,
		Port:             int64(hatchApp.TargetPort),
		ExecutionRoleArn: fmt.Sprintf("arn:aws:iam::%s:role/ecsTaskExecutionRole", payModel.AWSAccountId), // TODO: Make this configurable?
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
	taskDefResult, err := svc.CreateTaskDefinition(&taskDef, userName, hash, payModel.AWSAccountId)
	if err != nil {
		aerr := deleteAPIKeyWithContext(ctx, accessToken, apiKey.KeyID)
		if aerr != nil {
			Config.Logger.Printf("Error occurred when deleting API Key with ID %s for user %s: %s\n", apiKey.KeyID, userName, err.Error())
		}
		return err
	}

	Config.Logger.Printf("Setting up Transit Gateway for user %s", userName)
	go setupTransitGateway(userName)
	if err != nil {
		return err
	}

	Config.Logger.Printf("Launching ECS workspace service for user %s", userName)
	launchTask, err := svc.launchService(ctx, taskDefResult, userName, hash, payModel)
	if err != nil {
		aerr := deleteAPIKeyWithContext(ctx, accessToken, apiKey.KeyID)
		if aerr != nil {
			Config.Logger.Printf("Error occurred when deleting API Key with ID %s for user %s: %s\n", apiKey.KeyID, userName, err.Error())
		}
		return err
	}
	Config.Logger.Printf("Launched ECS workspace service at %s for user %s\n", launchTask, userName)
	return nil
}

// Launch ECS service for task definition + LB for routing
func (sess *CREDS) launchService(ctx context.Context, taskDefArn string, userName string, hash string, payModel PayModel) (string, error) {
	svc := sess.svc
	hatchApp := Config.ContainersMap[hash]
	cluster, err := sess.findEcsCluster()
	if err != nil {
		return "", err
	}
	Config.Logger.Printf("Cluster: %s", *cluster.ClusterName)

	networkConfig, err := sess.NetworkConfig(userName)
	if err != nil {
		return "", err
	}

	loadBalancer, targetGroupArn, _, err := sess.CreateLoadBalancer(userName)
	if err != nil {
		return "", err
	}
	svcName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + userToResourceName(userName, "pod") + "svc"
	input := &ecs.CreateServiceInput{
		DesiredCount:         aws.Int64(1),
		Cluster:              cluster.ClusterArn,
		ServiceName:          aws.String(svcName),
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
			case ecs.ErrCodeInvalidParameterException:
				if aerr.Error() == "InvalidParameterException: Creation of service was not idempotent." {
					Config.Logger.Print("Service already exists.. ")
					return "", nil
				} else {
					Config.Logger.Println(ecs.ErrCodeInvalidParameterException, aerr.Error())
				}
			}
		} else {

			Config.Logger.Println(err.Error())
			return "", err
		}
	}
	Config.Logger.Printf("Service launched: %s", *result.Service.ClusterArn)
	err = createLocalService(ctx, userName, hash, *loadBalancer.LoadBalancers[0].DNSName, payModel)
	if err != nil {
		return "", err
	}
	return *loadBalancer.LoadBalancers[0].DNSName, nil
}

// Create/Update Task Definition in ECS
func (sess *CREDS) CreateTaskDefinition(input *CreateTaskDefinitionInput, userName string, hash string, awsAcctID string) (string, error) {
	creds := sess.creds
	LogGroup, err := sess.CreateLogGroup(fmt.Sprintf("/hatchery/%s/", awsAcctID), creds)
	if err != nil {
		Config.Logger.Printf("Failed to create/get LogGroup. Error: %s", err)
		return "", err
	}
	svc := ecs.New(session.Must(session.NewSession(&aws.Config{
		Credentials: creds,
		Region:      aws.String("us-east-1"),
	})))

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

	containerDefinitions := []*ecs.ContainerDefinition{
		containerDefinition,
		&sidecarContainerDefinition,
	}

	if Config.Config.PrismaConfig.Enable {
		installBundle, err := getInstallBundle()
		if err != nil {
			Config.Logger.Print(err, " error getting prisma install bundle")
			return "", err
		}

		image, err := getPrismaImage()
		if err != nil {
			Config.Logger.Print(err, " error getting prisma image")
			return "", err
		}

		paloAltoContainerDefinition := ecs.ContainerDefinition{
			EntryPoint: aws.StringSlice([]string{
				"/usr/local/bin/defender",
				"fargate",
				"sidecar",
			}),
			Environment: []*ecs.KeyValuePair{
				{
					Name:  aws.String("INSTALL_BUNDLE"),
					Value: aws.String(installBundle.Bundle),
				},
				{
					Name:  aws.String("DEFENDER_TYPE"),
					Value: aws.String("fargate"),
				},
				{
					Name:  aws.String("FARGATE_TASK"),
					Value: aws.String(userName),
				},
				{
					Name: aws.String("WS_ADDRESS"),
					// TODO: Hardcoding in the address for now as the prisma api is returning wrong value
					Value: aws.String("wss://us-west1.cloud.twistlock.com:443"),
				},
				{
					Name:  aws.String("FILESYSTEM_MONITORING"),
					Value: aws.String("false"),
				},
			},
			Essential: aws.Bool(true),
			HealthCheck: &ecs.HealthCheck{
				Command: aws.StringSlice([]string{
					"/usr/local/bin/defender",
					"fargate",
					"healthcheck",
				}),
				Interval:    aws.Int64(5),
				Retries:     aws.Int64(3),
				StartPeriod: aws.Int64(1),
				Timeout:     aws.Int64(5),
			},
			Image:            aws.String(*image),
			Name:             aws.String("TwistlockDefender"),
			LogConfiguration: logConfiguration,
		}

		containerDefinitions = append(containerDefinitions, &paloAltoContainerDefinition)
	}

	resp, err := svc.RegisterTaskDefinition(
		&ecs.RegisterTaskDefinitionInput{
			ContainerDefinitions:    containerDefinitions,
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
