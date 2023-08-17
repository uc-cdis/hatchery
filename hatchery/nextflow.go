package hatchery

import (
	// "context"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/apparentlymart/go-cidr/cidr"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"

	// "github.com/aws/aws-sdk-go/aws/credentials" // TODO remove
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/batch"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/iam"
	"github.com/aws/aws-sdk-go/service/s3"
	// "github.com/aws/aws-sdk-go/service/s3/s3manager"
)

// create the per-user AWS resources required to launch nextflow workflows
func createNextflowUserResources(userName string, batchComputeEnvArn string) (string, string, error) {
	// // TODO get this working with paymodels
	// // roleARN := "arn:aws:iam::" + payModel.AWSAccountId + ":role/csoc_adminvm"
	// // sess := awstrace.WrapSession(session.Must(session.NewSession(&aws.Config{
	// // 	Region: aws.String(payModel.Region),
	// // })))
	// // creds := stscreds.NewCredentials(sess, roleARN)
	// sess := session.Must(session.NewSessionWithOptions(session.Options{
	// 	Config: aws.Config{
	// 		Region: aws.String("us-east-1"),
	// 		// Credentials: credentials.NewStaticCredentials(os.Getenv("AccessKeyId"), os.Getenv("SecretAccessKey"), ""),
	// 	},
	// }))
	// batchSvc := batch.New(sess)
	// iamSvc := iam.New(sess)

	payModel, err := getCurrentPayModel(userName)
	if err != nil {
		return "", "", err
	}

	// TODO: Make this configurable
	roleArn := "arn:aws:iam::" + payModel.AWSAccountId + ":role/csoc_adminvm"

	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))

	creds := stscreds.NewCredentials(sess, roleArn)
	awsConfig := aws.Config{
		Region:      aws.String("us-east-1"),
		Credentials: creds,
	}

	batchSvc := batch.New(sess, &awsConfig)
	iamSvc := iam.New(sess, &awsConfig)
	s3Svc := s3.New(sess, &awsConfig)
	userName = escapism(userName)
	hostname := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-")
	bucketName := fmt.Sprintf("%s--nextflow-%s", hostname, userName)
	Config.Logger.Print("Bucket name: ", bucketName)

	// set the tags we will use on all created resources
	// batch and iam accept different formats
	tag := fmt.Sprintf("%s--hatchery-nextflow--%s", hostname, userName)
	tagsMap := map[string]*string{
		"name": &tag,
	}
	tags := []*iam.Tag{
		&iam.Tag{
			Key:   aws.String("name"),
			Value: &tag,
		},
	}
	pathPrefix := aws.String(fmt.Sprintf("/%s/", tag))

	// Create s3 bucket
	err = setupNextflowS3bucket(s3Svc, userName, bucketName)
	if err != nil {
		Config.Logger.Print("Error creating s3 bucket: ", err)
		return "", "", err
	}

	// create AWS batch job queue
	batchJobQueueName := fmt.Sprintf("%s--nextflow-job-queue--%s", hostname, userName)
	_, err = batchSvc.CreateJobQueue(&batch.CreateJobQueueInput{
		JobQueueName: &batchJobQueueName,
		ComputeEnvironmentOrder: []*batch.ComputeEnvironmentOrder{
			{
				ComputeEnvironment: &batchComputeEnvArn,
				Order:              aws.Int64(int64(0)),
			},
		},
		Priority: aws.Int64(int64(0)),
		Tags:     tagsMap,
	})
	if err != nil {
		if strings.Contains(err.Error(), "Object already exists") {
			Config.Logger.Printf("Debug: Batch job queue '%s' already exists", batchJobQueueName)
		} else {
			Config.Logger.Printf("Error creating Batch job queue '%s': %v", batchJobQueueName, err)
			return "", "", err
		}
	} else {
		Config.Logger.Printf("Created Batch job queue '%s'", batchJobQueueName)
	}

	// create IAM policy for nextflow-created jobs
	policyName := fmt.Sprintf("%s--nextflow-jobs--%s", hostname, userName)
	nextflowJobsPolicyArn, err := createPolicyIfNotExist(iamSvc, policyName, pathPrefix, tags, aws.String(fmt.Sprintf(`{
		"Version": "2012-10-17",
		"Statement": [
			{
				"Effect": "Allow",
				"Action": [
					"s3:*"
				],
				"Resource": [
					"arn:aws:s3:::%s",
					"arn:aws:s3:::%s/%s/*"
				]
			},
			{
				"Effect": "Allow",
				"Action": [
					"s3:GetObject"
				],
				"Resource": [
					"*"
				]
			}
		]
	}`, bucketName, bucketName, userName)))
	if err != nil {
		return "", "", err
	}

	// create role for nextflow-created jobs
	roleName := policyName
	roleResult, err := iamSvc.CreateRole(&iam.CreateRoleInput{
		RoleName: &roleName,
		AssumeRolePolicyDocument: aws.String(`{
			"Version": "2012-10-17",
			"Statement": [
				{
					"Effect": "Allow",
					"Principal": {
						"Service": "ecs-tasks.amazonaws.com"
					},
					"Action": "sts:AssumeRole"
				}
			]
		}`),
		Path: pathPrefix, // so we can use the path later to get the role ARN
		Tags: tags,
	})
	nextflowJobsRoleArn := ""
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == iam.ErrCodeEntityAlreadyExistsException {
				Config.Logger.Printf("Debug: role '%s' already exists", roleName)
				listRolesResult, err := iamSvc.ListRoles(&iam.ListRolesInput{
					PathPrefix: pathPrefix,
				})
				if err != nil {
					Config.Logger.Printf("Error getting existing role '%s': %v", roleName, err)
					return "", "", err
				}
				nextflowJobsRoleArn = *listRolesResult.Roles[0].Arn
			} else {
				Config.Logger.Printf("Error creating role '%s': %v", roleName, aerr)
				return "", "", err
			}
		} else {
			Config.Logger.Printf("Error creating role '%s': %v", roleName, err)
			return "", "", err
		}
	} else {
		Config.Logger.Printf("Created role '%s'", roleName)
		nextflowJobsRoleArn = *roleResult.Role.Arn
	}

	// attach policy to role for nextflow-created jobs
	_, err = iamSvc.AttachRolePolicy(&iam.AttachRolePolicyInput{
		PolicyArn: &nextflowJobsPolicyArn,
		RoleName:  &roleName,
	})
	if err != nil {
		Config.Logger.Printf("Error attaching policy '%s' to role '%s': %v", policyName, roleName, err)
		return "", "", err
	} else {
		Config.Logger.Printf("Attached policy '%s' to role '%s'", policyName, roleName)
	}

	// create IAM policy for nextflow client
	policyName = fmt.Sprintf("%s--nextflow--%s", hostname, userName)
	nextflowPolicyArn, err := createPolicyIfNotExist(iamSvc, policyName, pathPrefix, tags, aws.String(fmt.Sprintf(`{
		"Version": "2012-10-17",
		"Statement": [
			{
				"Effect": "Allow",
				"Action": [
					"batch:SubmitJob",
					"batch:DescribeJobs",
					"batch:TerminateJob",
					"batch:RegisterJobDefinition",
					"batch:DescribeJobDefinitions",
					"batch:DeregisterJobDefinition",
					"batch:DescribeJobQueues",
					"batch:ListJobs",
					"s3:*"
				],
				"Resource": [
					"arn:aws:batch:*:*:job-definition/*",
					"arn:aws:batch:*:*:job-queue/%s",
					"arn:aws:s3:::%s",
					"arn:aws:s3:::%s/%s/*"
				]
			},
			{
				"Effect": "Allow",
				"Action": [
					"batch:*",
					"batch:DescribeJobDefinitions"
				],
				"Resource": [
					"*"
				]
			},
			{
				"Effect": "Allow",
				"Action": [
					"s3:ListBucket",
					"s3:GetObject"
				],
				"Resource": [
					"*"
				]
			},
			{
				"Effect": "Allow",
				"Action": [
					"iam:PassRole"
				],
				"Resource": [
					"%s"
				]
			}
		]
	}`, batchJobQueueName, bucketName, bucketName, userName, nextflowJobsRoleArn)))
	if err != nil {
		return "", "", err
	}

	// create user for nextflow client
	nextflowUserName := fmt.Sprintf("%s--nextflow--%s", hostname, userName)
	_, err = iamSvc.CreateUser(&iam.CreateUserInput{
		UserName: &nextflowUserName,
		Tags:     tags,
	})
	if err != nil {
		if strings.Contains(err.Error(), "EntityAlreadyExists") {
			Config.Logger.Printf("Debug: user '%s' already exists", nextflowUserName)
		} else {
			Config.Logger.Printf("Error creating user '%s': %v", nextflowUserName, err)
			return "", "", err
		}
	} else {
		Config.Logger.Printf("Created user '%s'", nextflowUserName)
	}

	// attach policy to user for nextflow client
	_, err = iamSvc.AttachUserPolicy(&iam.AttachUserPolicyInput{
		UserName:  &nextflowUserName,
		PolicyArn: &nextflowPolicyArn,
	})
	if err != nil {
		Config.Logger.Printf("Error attaching policy '%s' to user '%s': %v", policyName, nextflowUserName, err)
		return "", "", err
	} else {
		Config.Logger.Printf("Attached policy '%s' to user '%s'", policyName, nextflowUserName)
	}

	// create access key for the nextflow user
	accessKeyResult, err := iamSvc.CreateAccessKey(&iam.CreateAccessKeyInput{
		UserName: &nextflowUserName,
	})
	if err != nil {
		Config.Logger.Printf("Error creating access key for user '%s': %v", nextflowUserName, err)
		return "", "", err
	}
	keyId := *accessKeyResult.AccessKey.AccessKeyId
	keySecret := *accessKeyResult.AccessKey.SecretAccessKey
	Config.Logger.Printf("Created access key '%v' for user '%s'", keyId, nextflowUserName)

	// once we mount the configuration automatically, we can remove this log
	Config.Logger.Printf("CONFIGURATION: Batch queue: '%s'. Job role: '%s'. Workdir: '%s'.", batchJobQueueName, nextflowJobsRoleArn, fmt.Sprintf("s3://%s/%s", bucketName, userName))

	return keyId, keySecret, nil
}

// delete the per-user AWS resources created to launch nextflow workflows
func cleanUpNextflowUserResources(userName string) error {
	// sess := session.Must(session.NewSessionWithOptions(session.Options{
	// 	Config: aws.Config{
	// 		Region: aws.String("us-east-1"),
	// 		// Credentials: credentials.NewStaticCredentials(os.Getenv("AccessKeyId"), os.Getenv("SecretAccessKey"), ""),
	// 	},
	// }))

	payModel, err := getCurrentPayModel(userName)
	if err != nil {
		return err
	}

	// TODO: Make this configurable
	roleArn := "arn:aws:iam::" + payModel.AWSAccountId + ":role/csoc_adminvm"

	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))

	creds := stscreds.NewCredentials(sess, roleArn)
	awsConfig := aws.Config{
		Region:      aws.String("us-east-1"),
		Credentials: creds,
	}

	iamSvc := iam.New(sess, &awsConfig)
	// s3Svc := s3.New(sess)

	userName = escapism(userName)
	hostname := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-")

	// delete the user's access key
	nextflowUserName := fmt.Sprintf("%s--nextflow--%s", hostname, userName)
	listAccessKeysResult, err := iamSvc.ListAccessKeys(&iam.ListAccessKeysInput{
		UserName: &nextflowUserName,
	})
	if err != nil {
		Config.Logger.Printf("Unable to list access keys for user '%s': %v", nextflowUserName, err)
		return err
	}
	for _, key := range listAccessKeysResult.AccessKeyMetadata {
		Config.Logger.Printf("Deleting access key '%s' for user '%s'", *key.AccessKeyId, nextflowUserName)
		_, err := iamSvc.DeleteAccessKey(&iam.DeleteAccessKeyInput{
			UserName:    &nextflowUserName,
			AccessKeyId: key.AccessKeyId,
		})
		if err != nil {
			Config.Logger.Printf("Warning: Unable to delete access key '%s' for user '%s' - continuing: %v", *key.AccessKeyId, nextflowUserName, err)
		}
	}
	Config.Logger.Printf("Debug: Deleted access keys for Nextflow AWS user '%s'", nextflowUserName)

	// NOTE: This was disabled because researchers may need to keep the intermediary files. Instead of
	// deleting, we could set bucket lifecycle rules to delete after X days.
	// NOTE: The code below works locally but not once deployed

	// // delete the user's folder and its contents in the nextflow bucket
	// objectsKey := fmt.Sprintf("%s/", userName)
	// // objectsIter := s3manager.NewDeleteListIterator(s3Svc, &s3.ListObjectsInput{
	// // 	Bucket: &bucketName,
	// // 	Prefix: &objectsKey,
	// // })
	// objectsIter := s3manager.NewDeleteListIterator(s3Svc, &s3.ListObjectsInput{
	// 	Bucket: aws.String("qa-ibd-planx-pla-net--nextflow"),
	// 	Prefix: aws.String("ribeyre-40uchicago-2eedu/"),
	// })
	// if err := s3manager.NewBatchDeleteWithClient(s3Svc).Delete(context.Background(), objectsIter); err != nil {
	// 	Config.Logger.Printf("Unable to delete objects in bucket '%s' at '%s' - continuing: %v", bucketName, objectsKey, err)
	// } else {
	// 	Config.Logger.Printf("Debug: Deleted objects in bucket '%s' at '%s'", bucketName, objectsKey)
	// }

	return nil
}

// Create AWS BATCH compute environment for the user in users account.
func setupBatchComputeEnvironment(userName string) (*string, error) {

	payModel, err := getCurrentPayModel(userName)
	if err != nil {
		return nil, err
	}

	// TODO: Make this configurable
	roleArn := "arn:aws:iam::" + payModel.AWSAccountId + ":role/csoc_adminvm"

	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))

	creds := stscreds.NewCredentials(sess, roleArn)
	awsConfig := aws.Config{
		Region:      aws.String("us-east-1"),
		Credentials: creds,
	}

	batchSvc := batch.New(sess, &awsConfig)
	ec2Svc := ec2.New(sess, &awsConfig)
	iamSvc := iam.New(sess, &awsConfig)
	// s3Svc := s3.New(sess, &awsConfig)

	// set the tags we will use on all created resources
	// tag := fmt.Sprintf("%s--hatchery-nextflow", hostname)
	// tagsMap := map[string]*string{
	// 	"name": &tag,
	// }

	instanceProfile, err := createEcsInstanceProfile(iamSvc, "ecsInstanceRole")
	if err != nil {
		return nil, err
	}

	Config.Logger.Printf("Created ECS instance profile: %v", *instanceProfile)

	// hostname := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-")
	// create the VPC if it doesn't exist
	vpcid, subnetids, err := setupNextflowVPC(ec2Svc, userName)
	if err != nil {
		return nil, err
	}

	// create the compute environment
	batchEnv, err := createBatchComputeEnvironment(batchSvc, ec2Svc, *vpcid, *subnetids, userName)
	if err != nil {
		return nil, err
	}
	Config.Logger.Print("Created AWS Batch compute environment: ", &batchEnv)
	return batchEnv, nil

}

// Create VPC for aws batch compute environment
func setupNextflowVPC(ec2Svc *ec2.EC2, userName string) (*string, *[]string, error) {

	hostname := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-")

	// Subnets
	// TODO: make base CIDR configurable?
	cidrstring := "192.168.0.0/16"
	_, IPNet, _ := net.ParseCIDR(cidrstring)

	numberOfSubnets := 4
	// subnet cidr ranges in array
	subnets := []string{}
	subnetIds := []string{}
	// loop over the number of subnets and create them
	for i := 0; i < numberOfSubnets; i++ {
		subnet, err := cidr.Subnet(IPNet, 2, i)
		if err != nil {
			return nil, nil, err
		}
		subnetString := subnet.String()
		subnets = append(subnets, subnetString)
	}

	// create VPC
	vpcName := fmt.Sprintf("nextflow-%s-%s", userToResourceName(userName, "vpc"), hostname)

	descVPCInput := &ec2.DescribeVpcsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("cidr"),
				Values: []*string{aws.String(cidrstring)},
			},
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String(vpcName)},
			},
			{
				Name:   aws.String("tag:Environment"),
				Values: []*string{aws.String(os.Getenv("GEN3_ENDPOINT"))},
			},
		},
	}
	vpc, err := ec2Svc.DescribeVpcs(descVPCInput)
	if err != nil {
		return nil, nil, err
	}
	vpcid := ""
	// TODO: Check that VPC is configured correctly too, and not just the length of vpc's
	if len(vpc.Vpcs) == 0 {
		Config.Logger.Print("Debug: VPC does not exist, creating it now")
		vpc, err := createVPC(cidrstring, vpcName, ec2Svc)
		if err != nil {
			return nil, nil, err
		}
		// debug log the vpc
		Config.Logger.Printf("Debug: Created VPC: %v", vpc)

		vpcid = *vpc.Vpc.VpcId
	}

	vpcid = *vpc.Vpcs[0].VpcId

	// create internet gateway
	_, err = createInternetGW(vpcName, vpcid, ec2Svc)
	if err != nil {
		return nil, nil, err
	}

	// create subnets
	for i, subnet := range subnets {
		subnetName := fmt.Sprintf("nextflow-subnet-%d", i)
		Config.Logger.Print("Debug: Creating subnet: ", subnet, " with name: ", subnetName)

		createSubnetInput := &ec2.CreateSubnetInput{
			CidrBlock: aws.String(subnet),
			VpcId:     &vpcid,
			TagSpecifications: []*ec2.TagSpecification{
				{
					// Name
					ResourceType: aws.String("subnet"),
					Tags: []*ec2.Tag{
						{
							Key:   aws.String("Name"),
							Value: aws.String(subnetName),
						},
						{
							Key:   aws.String("Environment"),
							Value: aws.String(os.Getenv("GEN3_ENDPOINT")),
						},
					},
				},
			},
		}
		// Check if subnet exists
		descSubnetInput := &ec2.DescribeSubnetsInput{
			Filters: []*ec2.Filter{
				{
					Name:   aws.String("cidr-block"),
					Values: []*string{aws.String(subnet)},
				},
				{
					Name:   aws.String("tag:Name"),
					Values: []*string{aws.String(subnetName)},
				},
				{
					Name:   aws.String("tag:Environment"),
					Values: []*string{aws.String(os.Getenv("GEN3_ENDPOINT"))},
				},
			},
		}
		subnet, err := ec2Svc.DescribeSubnets(descSubnetInput)
		if err != nil {
			return nil, nil, err
		}
		if len(subnet.Subnets) > 0 {
			Config.Logger.Print("Debug: Subnet already exists, skipping creation")
			for _, subnet := range subnet.Subnets {
				subnetIds = append(subnetIds, *subnet.SubnetId)
				Config.Logger.Print("Debug: returning subnetid: ", *subnet.SubnetId)
			}
			continue
		}
		subnetOutput, err := ec2Svc.CreateSubnet(createSubnetInput)
		if err != nil {
			return nil, nil, err
		}

		subnetIds = append(subnetIds, *subnetOutput.Subnet.SubnetId)
		Config.Logger.Print("Debug: Created subnet: ", subnetOutput)
	}

	Config.Logger.Print("Debug: Nextflow VPC setup complete")
	return &vpcid, &subnetIds, nil
}

func createBatchComputeEnvironment(batchSvc *batch.Batch, ec2Svc *ec2.EC2, vpcID string, subnetids []string, userName string) (*string, error) {
	hostname := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-")
	batchComputeEnvName := fmt.Sprintf("%s-nextflow-compute-env", hostname)

	// Check if batch compute env exists, if it does return it
	descBatchComputeEnvInput := &batch.DescribeComputeEnvironmentsInput{
		ComputeEnvironments: []*string{
			aws.String(batchComputeEnvName),
		},
	}
	batchComputeEnv, err := batchSvc.DescribeComputeEnvironments(descBatchComputeEnvInput)
	if err != nil {
		return nil, err
	}
	if len(batchComputeEnv.ComputeEnvironments) > 0 {
		Config.Logger.Print("Debug: Batch compute environment already exists, skipping creation")
		return batchComputeEnv.ComputeEnvironments[0].ComputeEnvironmentArn, nil
	}

	// TODO: Configurable via hatcery config
	const batchComputeEnvMaxvCpus = 9

	payModel, err := getCurrentPayModel(userName)
	if err != nil {
		return nil, err
	}

	subnets := []*string{}

	for _, subnet := range subnetids {
		s := subnet
		Config.Logger.Print(s)
		subnets = append(subnets, &s)
	}

	Config.Logger.Print("Debug: Creating subnets: ", subnets)

	// set the tags we will use on all created resources
	// TODO: Proper tagging strategy
	tag := fmt.Sprintf("%s-hatchery-nextflow", hostname)
	tagsMap := map[string]*string{
		"name": &tag,
	}

	// Get the deafult security group for the VPC
	securityGroup, err := ec2Svc.DescribeSecurityGroups(&ec2.DescribeSecurityGroupsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []*string{aws.String(vpcID)},
			},
			{
				Name:   aws.String("group-name"),
				Values: []*string{aws.String("default")},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	securityGroupId := securityGroup.SecurityGroups[0].GroupId

	batchComputeEnvResult, err := batchSvc.CreateComputeEnvironment(&batch.CreateComputeEnvironmentInput{
		ComputeEnvironmentName: &batchComputeEnvName,
		// ServiceRole: "arn:aws:iam::707767160287:role/aws-service-role/batch.amazonaws.com/AWSServiceRoleForBatch",
		// ComputeEnvironmentOrder: []*batch.ComputeEnvironmentOrder{
		// 	{
		// 		ComputeEnvironment: aws.String("arn:aws:batch:us-east-1:707767160287:compute-environment/nextflow-pauline-compute-env"), // TODO update
		// 		Order: aws.Int64(int64(0)),
		// 	},
		// },
		// Priority: aws.Int64(int64(0)),
		Type: aws.String("MANAGED"), // TODO maybe using unmanaged allows users to choose the instance types? or does nextflow control that?
		ComputeResources: &batch.ComputeResource{
			Ec2Configuration: []*batch.Ec2Configuration{
				{
					ImageIdOverride: aws.String("ami-0069809e4eba54531"), // TODO generate dynamically or get from config
					ImageType:       aws.String("ECS_AL2"),
				},
			},
			InstanceRole:       aws.String(fmt.Sprintf("arn:aws:iam::%s:instance-profile/ecsInstanceRole", payModel.AWSAccountId)),
			AllocationStrategy: aws.String("BEST_FIT_PROGRESSIVE"),
			MinvCpus:           aws.Int64(int64(0)),
			MaxvCpus:           aws.Int64(int64(batchComputeEnvMaxvCpus)), // TODO: Configurable via hatchery config?
			InstanceTypes:      []*string{aws.String("optimal")},
			SecurityGroupIds:   []*string{securityGroupId}, // TODO
			Subnets:            subnets,
			Type:               aws.String("SPOT"), // TODO probably not - too slow
			Tags:               tagsMap,
		},
		Tags: tagsMap,
	})

	if err != nil {
		return nil, err
	}

	Config.Logger.Print("Debug: Created compute environment: ", batchComputeEnvResult)

	return batchComputeEnvResult.ComputeEnvironmentArn, nil
}

// Create IAM role for AWS Batch compute environment
func createEcsInstanceProfile(iamSvc *iam.IAM, name string) (*string, error) {
	Config.Logger.Print("Debug: Creating ECS instance profile: ", name)
	// Define the role policy
	rolePolicy := `{
		"Version": "2012-10-17",
		"Statement": [
		  {
			"Effect": "Allow",
			"Principal": { "Service": "ec2.amazonaws.com"},
			"Action": "sts:AssumeRole"
		  }
		]
	  }`

	// Create the IAM role
	_, err := iamSvc.CreateRole(&iam.CreateRoleInput{
		AssumeRolePolicyDocument: aws.String(rolePolicy),
		RoleName:                 aws.String(name),
	})
	// Handle error
	if err != nil {
		// if role exists move on
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == iam.ErrCodeEntityAlreadyExistsException {
			Config.Logger.Print("Debug: Role already exists, skipping creation")
		} else {
			return nil, err
		}

	}

	// Attach policy to the role
	_, err = iamSvc.AttachRolePolicy(&iam.AttachRolePolicyInput{
		PolicyArn: aws.String("arn:aws:iam::aws:policy/service-role/AmazonEC2ContainerServiceforEC2Role"),
		RoleName:  aws.String(name),
	})
	// Handle error
	if err != nil {
		return nil, err
	}

	instanceProfile, err := iamSvc.GetInstanceProfile(&iam.GetInstanceProfileInput{
		InstanceProfileName: aws.String(name),
	})

	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == iam.ErrCodeNoSuchEntityException {
			Config.Logger.Print("Debug: Instance profile does not exist, creating it now")
			// // Instance profile doesn't exist, create it
			instanceProfile, err := iamSvc.CreateInstanceProfile(&iam.CreateInstanceProfileInput{
				InstanceProfileName: aws.String(name),
			})
			if err != nil {
				return nil, err
			}

			return instanceProfile.InstanceProfile.Arn, nil
		}
		return nil, err
	}

	_, err = iamSvc.AddRoleToInstanceProfile(&iam.AddRoleToInstanceProfileInput{
		InstanceProfileName: aws.String(name),
		RoleName:            aws.String(name),
	})

	// Profile already exists
	return instanceProfile.InstanceProfile.Arn, nil
}

// Create s3 bucket for user
func setupNextflowS3bucket(s3Svc *s3.S3, userName string, bucketName string) error {
	// create S3 bucket for nextflow-created jobs
	_, err := s3Svc.CreateBucket(&s3.CreateBucketInput{
		Bucket: &bucketName,
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == s3.ErrCodeBucketAlreadyExists || aerr.Code() == s3.ErrCodeBucketAlreadyOwnedByYou {
				Config.Logger.Printf("Debug: S3 bucket '%s' already exists", bucketName)
			} else {
				Config.Logger.Printf("Error creating S3 bucket '%s': %v", bucketName, aerr)
				return err
			}
		} else {
			Config.Logger.Printf("Error creating S3 bucket '%s': %v", bucketName, err)
			return err
		}
	}
	return nil
}
