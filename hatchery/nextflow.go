package hatchery

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/apparentlymart/go-cidr/cidr"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/batch"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/iam"
	"github.com/aws/aws-sdk-go/service/imagebuilder"
	"github.com/aws/aws-sdk-go/service/kms"
	"github.com/aws/aws-sdk-go/service/s3"
)

/*
General TODOS:
- Make the AWS region configurable in the hatchery config (although ideally, the user should be able to choose) (MIDRC-743)
- Make the `roleArn` configurable (MIDRC-744)
- Can we do this long setup as a separate workspace launch step, instead of in the launch() function? (MIDRC-745)
*/

// create the AWS resources required to launch nextflow workflows
func createNextflowResources(userName string, nextflowGlobalConfig NextflowGlobalConfig, nextflowConfig NextflowConfig) (string, string, error) {
	var err error

	// credentials and AWS services init
	payModel, err := getCurrentPayModel(userName)
	if err != nil {
		return "", "", err
	}
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String("us-east-1"),
	}))
	awsAccountId, awsConfig, err := getNextflowAwsSettings(sess, payModel, userName, "creating")
	if err != nil {
		return "", "", err
	}
	Config.Logger.Printf("AWS account ID: '%v'", awsAccountId)
	batchSvc := batch.New(sess, &awsConfig)
	iamSvc := iam.New(sess, &awsConfig)
	s3Svc := s3.New(sess, &awsConfig)
	ec2Svc := ec2.New(sess, &awsConfig)
	kmsSvc := kms.New(sess, &awsConfig)

	userName = escapism(userName)
	hostname := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-")

	// The bucket name is not user-specific, but each user only has access to their prefix (`/username/*`) inside
	// the bucket. Bucket names are globally unique, so we add the AWS account ID so that each AWS account connected
	// to the environment can have its own Nextflow bucket - eg 1 bucket in main account for blanket billing workspaces,
	// 1 bucket in userA's account for userA's direct pay workspace, etc.
	bucketName := fmt.Sprintf("%s-nf-%s", hostname, awsAccountId)

	// set the tags we will use on all created resources.
	// different services accept different formats
	// TODO The VPC, subnets, route tables and squid instance do not have the
	// same tag as the other resources, so we can't use the same tag to track
	// costs. To use the same tag, we might need to update `vpc.go`. (MIDRC-746)
	tagWithUsername := fmt.Sprintf("%s-hatchery-nf-%s", hostname, userName)
	tagWithoutUsername := fmt.Sprintf("%s-hatchery-nf", hostname)
	// TODO Add more tags: "Environment" and possibly "gen3username" (MIDRC-746)
	tagsMap := map[string]*string{
		"Name": &tagWithUsername,
	}
	iamTags := []*iam.Tag{
		{
			Key:   aws.String("Name"),
			Value: &tagWithUsername,
		},
	}
	kmsTags := []*kms.Tag{
		{
			TagKey:   aws.String("Name"),
			TagValue: &tagWithoutUsername,
		},
	}
	pathPrefix := aws.String(fmt.Sprintf("/%s/", tagWithUsername))

	s3BucketWhitelistCondition := "" // if not configured, no buckets are allowed
	if len(nextflowConfig.S3BucketWhitelist) > 0 {
		s3BucketWhitelist := ""
		for _, bucket := range nextflowConfig.S3BucketWhitelist {
			if s3BucketWhitelist != "" {
				s3BucketWhitelist += ", "
			}
			s3BucketWhitelist += fmt.Sprintf("\"arn:aws:s3:::%s\", \"arn:aws:s3:::%s/*\"", bucket, bucket)
		}
		s3BucketWhitelistCondition = fmt.Sprintf(`,
		{
			"Sid": "AllowWhitelistedBuckets",
			"Effect": "Allow",
			"Action": [
				"s3:GetObject",
				"s3:ListBucket"
			],
			"Resource": [
				%s
			]
		}`, s3BucketWhitelist)
	}

	// create the VPC if it doesn't exist
	// launch squid
	// TODO: read the squid instance type from the hatchery config (would need to change
	// `launchSquidInstance` function to update the instance type if the instance already
	// exists) (MIDRC-751)
	squidInstanceType := "t2.micro"
	vpcid, subnetids, err := setupVpcAndSquid(ec2Svc, userName, hostname, nextflowConfig.InstanceType, squidInstanceType)
	if err != nil {
		Config.Logger.Printf("Unable to setup VPC: %v", err)
		return "", "", err
	}

	// Create nextflow compute environment if it does not exist
	batchComputeEnvArn, err := createBatchComputeEnvironment(nextflowGlobalConfig, nextflowConfig, userName, hostname, tagsMap, batchSvc, ec2Svc, iamSvc, *vpcid, *subnetids)
	if err != nil {
		Config.Logger.Printf("Error creating compute environment for user %s: %s", userName, err.Error())
		return "", "", err
	}

	// Create S3 bucket
	kmsKeyArn, err := createS3bucket(nextflowGlobalConfig, s3Svc, kmsSvc, bucketName, kmsTags)
	if err != nil {
		Config.Logger.Printf("Error creating S3 bucket '%s': %v", bucketName, err)
		return "", "", err
	}

	// create AWS batch job queue
	batchJobQueueName := fmt.Sprintf("%s-nf-job-queue-%s", hostname, userName)
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
	policyName := fmt.Sprintf("%s-nf-jobs-%s", hostname, userName)
	nextflowJobsPolicyArn, err := createOrUpdatePolicy(iamSvc, policyName, pathPrefix, iamTags, aws.String(fmt.Sprintf(`{
		"Version": "2012-10-17",
		"Statement": [
			{
				"Sid": "AllowListingBucketFolder",
				"Effect": "Allow",
				"Action": [
					"s3:ListBucket"
				],
				"Resource": [
					"arn:aws:s3:::%s"
				],
				"Condition": {
					"StringLike": {
						"s3:prefix": [
							"%s/*"
						]
					}
				}
			},
			{
				"Sid": "AllowManagingBucketFolder",
				"Effect": "Allow",
				"Action": [
					"s3:GetObject",
					"s3:PutObject",
					"s3:DeleteObject"
				],
				"Resource": [
					"arn:aws:s3:::%s/%s/*"
				]
			},
			{
				"Sid": "BucketEncryption",
				"Effect": "Allow",
				"Action": [
					"kms:GenerateDataKey",
					"kms:Decrypt"
				],
				"Resource": [
					"%s"
				]
			}
			%s
		]
	}`, bucketName, userName, bucketName, userName, kmsKeyArn, s3BucketWhitelistCondition)))
	if err != nil {
		return "", "", err
	}

	// create role for nextflow-created jobs
	roleName := truncateString(policyName, 64)
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
		Tags: iamTags,
	})
	nextflowJobsRoleArn := ""
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == iam.ErrCodeEntityAlreadyExistsException {
				Config.Logger.Printf("Debug: role '%s' already exists", roleName)
				listRolesResult, err := iamSvc.ListRoles(&iam.ListRolesInput{
					PathPrefix: pathPrefix,
				})
				if err != nil || len(listRolesResult.Roles) == 0 {
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
	// Note: `batch:DescribeComputeEnvironments` is listed as required
	// in the Nextflow docs, but it seems to work fine without it.
	policyName = fmt.Sprintf("%s-nf-%s", hostname, userName)
	jobImageWhitelistCondition := "" // if not configured, all images are allowed
	if len(nextflowConfig.JobImageWhitelist) > 0 {
		jobImageWhitelist := fmt.Sprintf(`"%v"`, strings.Join(replaceAllUsernamePlaceholders(nextflowConfig.JobImageWhitelist, userName), "\", \""))
		jobImageWhitelistCondition = fmt.Sprintf(`,
		"Condition": {
			"StringLike": {
				"batch:Image": [
					%s
				]
			}
		}`, jobImageWhitelist)
	}
	nextflowPolicyArn, err := createOrUpdatePolicy(iamSvc, policyName, pathPrefix, iamTags, aws.String(fmt.Sprintf(`{
		"Version": "2012-10-17",
		"Statement": [
			{
				"Sid": "AllowPassingNextflowJobsRole",
				"Effect": "Allow",
				"Action": [
					"iam:PassRole"
				],
				"Resource": [
					"%s"
				]
			},
			{
				"Sid": "AllowBatchActionsWithGranularAuthz",
				"Effect": "Allow",
				"Action": [
					"batch:DescribeJobQueues",
					"batch:ListJobs",
					"batch:SubmitJob",
					"batch:CancelJob",
					"batch:TerminateJob",
     					"batch:TagResource"
				],
				"Resource": [
					"arn:aws:batch:*:*:job-definition/*",
					"arn:aws:batch:*:*:job-queue/%s"
				]
			},
			{
				"Sid": "AllowBatchActionsWithoutGranularAuthz",
				"Effect": "Allow",
				"Action": [
					"batch:DescribeJobs",
					"batch:DescribeJobDefinitions"
				],
				"Resource": [
					"*"
				]
			},
			{
				"Sid": "AllowWhitelistedImages",
				"Effect": "Allow",
				"Action": [
					"batch:RegisterJobDefinition"
				],
				"Resource": [
					"arn:aws:batch:*:*:job-definition/*"
				]
				%s
			},
			{
				"Sid": "AllowListingBucketFolder",
				"Effect": "Allow",
				"Action": [
					"s3:ListBucket"
				],
				"Resource": [
					"arn:aws:s3:::%s"
				],
				"Condition": {
					"StringLike": {
						"s3:prefix": [
							"%s/*"
						]
					}
				}
			},
			{
				"Sid": "AllowManagingBucketFolder",
				"Effect": "Allow",
				"Action": [
					"s3:GetObject",
					"s3:PutObject",
					"s3:DeleteObject"
				],
				"Resource": [
					"arn:aws:s3:::%s/%s/*"
				]
			},
			{
				"Sid": "BucketEncryption",
				"Effect": "Allow",
				"Action": [
					"kms:GenerateDataKey",
					"kms:Decrypt"
				],
				"Resource": [
					"%s"
				]
			}
			%s
		]
	}`, nextflowJobsRoleArn, batchJobQueueName, jobImageWhitelistCondition, bucketName, userName, bucketName, userName, kmsKeyArn, s3BucketWhitelistCondition)))
	if err != nil {
		return "", "", err
	}

	// create user for nextflow client
	nextflowUserName := fmt.Sprintf("%s-nf-%s", hostname, userName)
	_, err = iamSvc.CreateUser(&iam.CreateUserInput{
		UserName: &nextflowUserName,
		Tags:     iamTags,
	})
	if err != nil {
		if strings.Contains(err.Error(), "EntityAlreadyExists") {
			Config.Logger.Printf("Debug: user '%s' already exists", nextflowUserName)

			// delete any existing access keys to avoid `LimitExceeded: Cannot exceed
			// quota for AccessKeysPerUser: 2` error
			err = deleteUserAccessKeys(nextflowUserName, iamSvc.ListAccessKeys, iamSvc.DeleteAccessKey)
			if err != nil {
				Config.Logger.Printf("Unable to delete access keys for user '%s': %v", nextflowUserName, err)
				return "", "", err
			}

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

	return keyId, keySecret, nil
}

var getNextflowAwsSettings = func(sess *session.Session, payModel *PayModel, userName string, action string) (string, aws.Config, error) {
	// credentials and AWS services init
	var awsConfig aws.Config
	var awsAccountId string
	if payModel != nil && payModel.Ecs {
		Config.Logger.Printf("Info: pay model enabled for user '%s': %s Nextflow resources in user's AWS account", userName, action)
		roleArn := fmt.Sprintf("arn:aws:iam::%s:role/csoc_adminvm", payModel.AWSAccountId)
		awsConfig = aws.Config{
			Credentials: stscreds.NewCredentials(sess, roleArn),
		}
		awsAccountId = payModel.AWSAccountId
	} else {
		Config.Logger.Printf("Info: pay model disabled for user '%s': %s Nextflow resources in main AWS account", userName, action)
		awsConfig = aws.Config{}
		Config.Logger.Printf("Debug: Getting AWS account ID...")
		var err error
		awsAccountId, err = getAwsAccountId(sess, &awsConfig)
		if err != nil {
			Config.Logger.Printf("Error getting AWS account ID: %v", err)
			return "", awsConfig, err
		}
	}
	return awsAccountId, awsConfig, nil
}

// Create VPC for aws batch compute environment
func setupVpcAndSquid(ec2Svc *ec2.EC2, userName string, hostname string, computeEnvInstanceType string, squidInstanceType string) (*string, *[]string, error) {
	// TODO: make base CIDR configurable? (MIDRC-747)
	cidrstring := "192.168.0.0/16"
	_, IPNet, _ := net.ParseCIDR(cidrstring)
	numberOfSubnets := 3
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

	// create the VPC
	// The VPC is per-user because the Squid architecture would not work with multiple users sharing a VPC, as
	// it follows the lifecycle of the workspace. Idle VPCs don’t cost anything so we can create one per user.
	vpcName := fmt.Sprintf("%s-nf-vpc-%s", hostname, userName)
	vpc, err := ec2Svc.DescribeVpcs(&ec2.DescribeVpcsInput{
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
	})
	if err != nil {
		return nil, nil, err
	}
	vpcid := ""
	// TODO: Check that the VPC is configured correctly, and not just that it exists (MIDRC-748)
	if len(vpc.Vpcs) == 0 {
		Config.Logger.Print("Debug: VPC does not exist, creating it now")
		vpc, err := createVPC(cidrstring, vpcName, ec2Svc)
		if err != nil {
			return nil, nil, err
		}
		Config.Logger.Printf("Debug: Created VPC '%s'", vpcName)

		vpcid = *vpc.Vpc.VpcId
	} else {
		vpcid = *vpc.Vpcs[0].VpcId
	}

	// create internet gateway
	igw, err := createInternetGW(vpcName, vpcid, ec2Svc)
	if err != nil {
		return nil, nil, err
	}

	// create subnets
	for i, subnet := range subnets {
		subnetName := fmt.Sprintf("%s-nf-subnet-%s-%d", hostname, userName, i)
		subnetId, err := setupSubnet(subnetName, subnet, vpcid, ec2Svc, computeEnvInstanceType)
		if err != nil {
			return nil, nil, err
		}
		subnetIds = append(subnetIds, *subnetId)
	}

	// setup route table for regular subnets
	routeTableId, err := setupRouteTable(hostname, userName, ec2Svc, vpcid, *igw, fmt.Sprintf("%s-nf-rt-%s", hostname, userName))
	if err != nil {
		return nil, nil, err
	}

	// setup route table for Squid subnet
	fwRouteTableId, err := setupRouteTable(hostname, userName, ec2Svc, vpcid, *igw, fmt.Sprintf("%s-nf-fw-rt-%s", hostname, userName))
	if err != nil {
		return nil, nil, err
	}

	// associate subnets with route table
	err = associateRouteTablesToSubnets(ec2Svc, subnetIds, *routeTableId)
	if err != nil {
		return nil, nil, err
	}

	// setup Squid
	fwSubnetId, err := setupSquid(hostname, userName, cidrstring, ec2Svc, vpcid, igw, fwRouteTableId, routeTableId, squidInstanceType)
	if err != nil {
		return nil, nil, err
	}
	Config.Logger.Printf("Debug: Created Squid '%s'", *fwSubnetId)

	Config.Logger.Print("Debug: Nextflow VPC setup complete")
	return &vpcid, &subnetIds, nil
}

// Function to make sure launch template is created, and configured correctly
// We need a launch template since we need a user data script to authenticate with private ECR repositories
func ensureLaunchTemplate(ec2Svc *ec2.EC2, userName string, hostname string, jobImageWhitelist []string) (*string, error) {
	// user data script to authenticate with private ECR repositories
	userData := generateEcrLoginUserData(jobImageWhitelist, userName)

	launchTemplateName := fmt.Sprintf("%s-nf-%s", hostname, userName)

	Config.Logger.Printf("Debug: Launch template name: %s", launchTemplateName)

	// create launch template
	launchTemplate, err := ec2Svc.DescribeLaunchTemplates(&ec2.DescribeLaunchTemplatesInput{
		LaunchTemplateNames: []*string{
			aws.String(launchTemplateName),
		},
	})
	if err != nil {
		// If no launch template exists, create it
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == "InvalidLaunchTemplateName.NotFoundException" {
			Config.Logger.Printf("Debug: Launch template '%s' does not exist, creating it", launchTemplateName)
			launchTemplate, err := ec2Svc.CreateLaunchTemplate(&ec2.CreateLaunchTemplateInput{
				LaunchTemplateName: aws.String(launchTemplateName),
				LaunchTemplateData: &ec2.RequestLaunchTemplateData{ // if changed, need to update launch template and compute env
					UserData: aws.String(userData),
				},
			})
			if err != nil {
				Config.Logger.Printf("Error creating launch template '%s': %v", launchTemplateName, err)
				return nil, err
			}
			Config.Logger.Printf("Debug: Created launch template '%s'", launchTemplateName)
			return launchTemplate.LaunchTemplate.LaunchTemplateName, nil
		} else {
			Config.Logger.Printf("Error describing launch template '%s': %v", launchTemplateName, err)
		}
		return nil, err
	}

	if len(launchTemplate.LaunchTemplates) == 1 {
		// TODO: Make sure user data in the existing launch template matches the user data
		// we want (MIDRC-749)
		Config.Logger.Printf("Debug: Launch template '%s' already exists", launchTemplateName)
		return launchTemplate.LaunchTemplates[0].LaunchTemplateName, nil
	}
	return nil, fmt.Errorf("more than one launch template with the same name exist: %v", launchTemplate.LaunchTemplates)
}

// Create AWS Batch compute environment
func createBatchComputeEnvironment(nextflowGlobalConfig NextflowGlobalConfig, nextflowConfig NextflowConfig, userName string, hostname string, tagsMap map[string]*string, batchSvc *batch.Batch, ec2Svc *ec2.EC2, iamSvc *iam.IAM, vpcid string, subnetids []string) (string, error) {
	instanceProfileArn, err := createEcsInstanceProfile(iamSvc, fmt.Sprintf("%s-nf-ecsInstanceRole", hostname))
	if err != nil {
		Config.Logger.Printf("Unable to create ECS instance profile: %s", err.Error())
		return "", err
	}

	// the launch template for the compute envrionment must be user-specific as well
	launchTemplateName, err := ensureLaunchTemplate(ec2Svc, userName, hostname, nextflowConfig.JobImageWhitelist)
	if err != nil {
		return "", err
	}

	// the compute environment must be user-specific as well, since it's in the user-specific VPC
	batchComputeEnvName := fmt.Sprintf("%s-nf-compute-env-%s", hostname, userName)

	// Check if batch compute env exists, if it does return it
	batchComputeEnv, err := batchSvc.DescribeComputeEnvironments(&batch.DescribeComputeEnvironmentsInput{
		ComputeEnvironments: []*string{
			aws.String(batchComputeEnvName),
		},
	})
	if err != nil {
		return "", err
	}

	// Configure the specified AMI. At the time of writing, CPU workflows launch on ECS_AL2 (default for all
	// non-GPU instances) and GPU workflows on ECS_AL2_NVIDIA (default for all GPU instances). Setting the AMI
	// for both types is easier than switching the image type based on which AMI (CPU or GPU) is configured.
	ami, err := getNextflowInstanceAmi(nextflowGlobalConfig.ImageBuilderReaderRoleArn, nextflowConfig, nil)
	if err != nil {
		return "", err
	}
	ec2Configuration := []*batch.Ec2Configuration{
		{
			ImageIdOverride: aws.String(ami),
			ImageType:       aws.String("ECS_AL2"),
		},
		{
			ImageIdOverride: aws.String(ami),
			ImageType:       aws.String("ECS_AL2_NVIDIA"),
		},
	}

	var batchComputeEnvArn string
	if len(batchComputeEnv.ComputeEnvironments) > 0 {
		Config.Logger.Printf("Debug: Batch compute environment '%s' already exists, updating it", batchComputeEnvName)
		batchComputeEnvArn = *batchComputeEnv.ComputeEnvironments[0].ComputeEnvironmentArn

		// wait for the compute env to be ready to be updated
		err = waitForBatchComputeEnvironment(batchComputeEnvName, batchSvc, false)
		if err != nil {
			return "", err
		}

		subnets := []*string{}
		for _, subnet := range subnetids {
			s := subnet
			subnets = append(subnets, &s)
		}

		// update any settings that may have changed in the config
		// TODO also make sure it is pointing at the correct subnets - if the VPC is deleted,
		// we should recreate the compute environment as well because it will be pointing at
		// old vpc subnets (MIDRC-750)
		_, err = batchSvc.UpdateComputeEnvironment(&batch.UpdateComputeEnvironmentInput{
			ComputeEnvironment: &batchComputeEnvArn,
			State:              aws.String("ENABLED"), // since the env already exists, make sure it's enabled
			ComputeResources: &batch.ComputeResourceUpdate{
				Ec2Configuration: ec2Configuration,
				LaunchTemplate: &batch.LaunchTemplateSpecification{
					LaunchTemplateName: launchTemplateName,
					Version:            aws.String("$Latest"),
				},
				InstanceRole:       instanceProfileArn,
				AllocationStrategy: aws.String("BEST_FIT_PROGRESSIVE"),
				MinvCpus:           aws.Int64(int64(nextflowConfig.InstanceMinVCpus)),
				MaxvCpus:           aws.Int64(int64(nextflowConfig.InstanceMaxVCpus)),
				InstanceTypes:      []*string{aws.String(nextflowConfig.InstanceType)},
				Subnets:            subnets,
				Type:               aws.String(nextflowConfig.ComputeEnvironmentType),
				Tags:               tagsMap,
			},
			UpdatePolicy: &batch.UpdatePolicy{
				// existing jobs are not terminated and keep running for up to 30 min after this update
				JobExecutionTimeoutMinutes: aws.Int64(30),
				TerminateJobsOnUpdate:      aws.Bool(false),
			},
		})
		if err != nil {
			Config.Logger.Printf("Unable to update Batch compute environment '%s': %v", batchComputeEnvName, err)
			return "", err
		}
	} else { // compute environment does not exist, create it
		Config.Logger.Printf("Debug: Batch compute environment '%s' does not exist, creating it", batchComputeEnvName)
		subnets := []*string{}
		for _, subnet := range subnetids {
			s := subnet
			subnets = append(subnets, &s)
		}

		// Get the default security group for the VPC
		securityGroup, err := ec2Svc.DescribeSecurityGroups(&ec2.DescribeSecurityGroupsInput{
			Filters: []*ec2.Filter{
				{
					Name:   aws.String("vpc-id"),
					Values: []*string{aws.String(vpcid)},
				},
				{
					Name:   aws.String("group-name"),
					Values: []*string{aws.String("default")},
				},
			},
		})
		if err != nil {
			return "", err
		}
		securityGroupId := securityGroup.SecurityGroups[0].GroupId

		batchComputeEnvResult, err := batchSvc.CreateComputeEnvironment(&batch.CreateComputeEnvironmentInput{
			ComputeEnvironmentName: &batchComputeEnvName,
			Type:                   aws.String("MANAGED"),
			ComputeResources: &batch.ComputeResource{
				Ec2Configuration: ec2Configuration,
				LaunchTemplate: &batch.LaunchTemplateSpecification{
					LaunchTemplateName: launchTemplateName,
					Version:            aws.String("$Latest"),
				},
				InstanceRole:       instanceProfileArn,
				AllocationStrategy: aws.String("BEST_FIT_PROGRESSIVE"),
				MinvCpus:           aws.Int64(int64(nextflowConfig.InstanceMinVCpus)),
				MaxvCpus:           aws.Int64(int64(nextflowConfig.InstanceMaxVCpus)),
				InstanceTypes:      []*string{aws.String(nextflowConfig.InstanceType)},
				SecurityGroupIds:   []*string{securityGroupId},
				Subnets:            subnets,
				Type:               aws.String(nextflowConfig.ComputeEnvironmentType),
				Tags:               tagsMap,
			},
			Tags: tagsMap,
		})
		if err != nil {
			return "", err
		}

		Config.Logger.Printf("Debug: Created AWS Batch compute environment '%s'", batchComputeEnvName)
		batchComputeEnvArn = *batchComputeEnvResult.ComputeEnvironmentArn
	}

	// the compute environment must be "VALID" before we can create the job queue: wait until ready
	err = waitForBatchComputeEnvironment(batchComputeEnvName, batchSvc, true)
	if err != nil {
		return "", err
	}

	return batchComputeEnvArn, nil
}

func waitForBatchComputeEnvironment(batchComputeEnvName string, batchSvc *batch.Batch, mustBeValid bool) error {
	maxIter := 6
	iterDelaySecs := 5
	var compEnvStatus string
	for i := 0; ; i++ {
		batchComputeEnvs, err := batchSvc.DescribeComputeEnvironments(&batch.DescribeComputeEnvironmentsInput{
			ComputeEnvironments: []*string{
				aws.String(batchComputeEnvName),
			},
		})
		if err != nil {
			return err
		}
		compEnvStatus = *batchComputeEnvs.ComputeEnvironments[0].Status
		// possible statuses: CREATING | UPDATING | DELETING | DELETED | VALID | INVALID
		if compEnvStatus == "VALID" {
			Config.Logger.Print("Debug: Compute environment is ready")
			break
		}
		if !mustBeValid && compEnvStatus == "INVALID" {
			Config.Logger.Printf("Debug: Compute environment is %s and can't be used, but can be updated", compEnvStatus)
			break
		}
		if i == maxIter {
			return fmt.Errorf("compute environment is not ready after %v seconds. Exiting", maxIter*iterDelaySecs)
		}
		Config.Logger.Printf("Info: Compute environment is %s, waiting %vs and checking again", compEnvStatus, iterDelaySecs)
		time.Sleep(time.Duration(iterDelaySecs) * time.Second)
	}
	return nil
}

// Create IAM role for AWS Batch compute environment
func createEcsInstanceProfile(iamSvc *iam.IAM, name string) (*string, error) {
	Config.Logger.Printf("Debug: Creating ECS instance profile '%s'", name)

	instanceProfile, err := iamSvc.GetInstanceProfile(&iam.GetInstanceProfileInput{
		InstanceProfileName: aws.String(name),
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == iam.ErrCodeNoSuchEntityException {
			Config.Logger.Printf("Debug: Instance profile '%s' does not exist, creating it", name)
			_, err = iamSvc.CreateInstanceProfile(&iam.CreateInstanceProfileInput{
				InstanceProfileName: aws.String(name),
			})
			if err != nil {
				return nil, err
			}
			instanceProfile, err = iamSvc.GetInstanceProfile(&iam.GetInstanceProfileInput{
				InstanceProfileName: aws.String(name),
			})
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	// Create the IAM role
	Config.Logger.Printf("Debug: Creating IAM role '%s'", name)
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
	_, err = iamSvc.CreateRole(&iam.CreateRoleInput{
		AssumeRolePolicyDocument: aws.String(rolePolicy),
		RoleName:                 aws.String(name),
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == iam.ErrCodeEntityAlreadyExistsException {
			Config.Logger.Printf("Debug: Role '%s' already exists, assuming it is already linked to instance profile and continuing", name)
			return instanceProfile.InstanceProfile.Arn, nil
		} else {
			Config.Logger.Printf("Unable to create IAM role '%s': %v", name, err)
			return nil, err
		}
	}

	// Attach policy to the role
	_, err = iamSvc.AttachRolePolicy(&iam.AttachRolePolicyInput{
		PolicyArn: aws.String("arn:aws:iam::aws:policy/service-role/AmazonEC2ContainerServiceforEC2Role"),
		RoleName:  aws.String(name),
	})
	if err != nil {
		return nil, err
	}

	_, err = iamSvc.AddRoleToInstanceProfile(&iam.AddRoleToInstanceProfileInput{
		InstanceProfileName: aws.String(name),
		RoleName:            aws.String(name),
	})
	if err != nil {
		Config.Logger.Printf("Unable to add role '%s' to instance profile '%s': %s", name, name, err.Error())
		return nil, err
	}

	Config.Logger.Printf("Info: Set up ECS instance profile '%s'", name)
	return instanceProfile.InstanceProfile.Arn, nil
}

func createS3bucket(nextflowGlobalConfig NextflowGlobalConfig, s3Svc *s3.S3, kmsSvc *kms.KMS, bucketName string, kmsTags []*kms.Tag) (string, error) {
	// create S3 bucket for nextflow input, output and intermediate files
	_, err := s3Svc.CreateBucket(&s3.CreateBucketInput{
		Bucket: &bucketName,
		// TODO We may need to add the LocationConstraint below if we change the region to not
		// "us-east-1". It seems this block causes an error when the region is "us-east-1", so
		// it would need to be added conditionally. (MIDRC-743)
		// CreateBucketConfiguration: &s3.CreateBucketConfiguration{
		// 	LocationConstraint: aws.String("us-east-1"),
		// },
	})
	if err != nil {
		// no need to check for a specific "bucket already exists" error since
		// `s3Svc.CreateBucket` does not error when the bucket exists
		Config.Logger.Printf("Error creating S3 bucket '%s': %v", bucketName, err)
		return "", err
	}
	Config.Logger.Printf("INFO: Created S3 bucket '%s'", bucketName)

	// set up KMS encryption on the bucket.
	// the only way to check if the KMS key has already been created is to use an alias
	kmsKeyAlias := fmt.Sprintf("alias/key-%s", bucketName)
	kmsDescribeKeyOutput, err := kmsSvc.DescribeKey(&kms.DescribeKeyInput{
		KeyId: aws.String(kmsKeyAlias),
	})
	var kmsKeyArn *string
	if err == nil {
		kmsKeyArn = kmsDescribeKeyOutput.KeyMetadata.Arn
		Config.Logger.Printf("DEBUG: Existing KMS key: '%s' - '%s'", kmsKeyAlias, *kmsKeyArn)
	} else {
		// if the KMS key doesn't exist, create it
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == "NotFoundException" {
			kmsCreateKeyOutput, err := kmsSvc.CreateKey(&kms.CreateKeyInput{
				Tags: kmsTags,
			})
			if err != nil {
				Config.Logger.Printf("Error creating KMS key: %v", err)
				return "", err
			}
			kmsKeyArn = kmsCreateKeyOutput.KeyMetadata.Arn
			Config.Logger.Printf("INFO: Created KMS key: '%s'", *kmsKeyArn)

			_, err = kmsSvc.CreateAlias(&kms.CreateAliasInput{
				AliasName:   &kmsKeyAlias,
				TargetKeyId: kmsKeyArn,
			})
			if err != nil {
				Config.Logger.Printf("Error creating KMS key alias: %v", err)
				return "", err
			}
			Config.Logger.Printf("INFO: Created KMS key alias: '%s'", kmsKeyAlias)
		} else {
			Config.Logger.Printf("Error describing existing KMS key '%s': %v", kmsKeyAlias, err)
			return "", err
		}
	}

	Config.Logger.Printf("DEBUG: Setting KMS encryption on bucket '%s'", bucketName)
	_, err = s3Svc.PutBucketEncryption(&s3.PutBucketEncryptionInput{
		Bucket: &bucketName,
		ServerSideEncryptionConfiguration: &s3.ServerSideEncryptionConfiguration{
			Rules: []*s3.ServerSideEncryptionRule{
				{
					ApplyServerSideEncryptionByDefault: &s3.ServerSideEncryptionByDefault{
						SSEAlgorithm:   aws.String("aws:kms"),
						KMSMasterKeyID: kmsKeyArn,
					},
					BucketKeyEnabled: aws.Bool(true),
				},
			},
		},
	})
	if err != nil {
		Config.Logger.Printf("Unable to set bucket encryption: %v", err)
		return "", err
	}

	Config.Logger.Printf("DEBUG: Enforcing KMS encryption through bucket policy")
	_, err = s3Svc.PutBucketPolicy(&s3.PutBucketPolicyInput{
		Bucket: &bucketName,
		Policy: aws.String(fmt.Sprintf(`{
			"Version": "2012-10-17",
			"Statement": [
				{
					"Sid": "RequireKMSEncryption",
					"Effect": "Deny",
					"Principal": "*",
					"Action": "s3:PutObject",
					"Resource": "arn:aws:s3:::%s/*",
					"Condition": {
						"StringNotLikeIfExists": {
							"s3:x-amz-server-side-encryption-aws-kms-key-id": "%s"
						}
					}
				}
			]
		}`, bucketName, *kmsKeyArn)),
	})
	if err != nil {
		Config.Logger.Printf("Unable to set bucket policy: %v", err)
		return "", err
	}

	expirationDays := nextflowGlobalConfig.S3ObjectsExpirationDays
	if expirationDays <= 0 {
		expirationDays = 30
	}
	Config.Logger.Printf("DEBUG: Setting bucket objects expiration to %d days", expirationDays)
	_, err = s3Svc.PutBucketLifecycleConfiguration(&s3.PutBucketLifecycleConfigurationInput{
		Bucket: &bucketName,
		LifecycleConfiguration: &s3.BucketLifecycleConfiguration{
			Rules: []*s3.LifecycleRule{
				{
					Expiration: &s3.LifecycleExpiration{
						Days: aws.Int64(int64(expirationDays)),
					},
					Status: aws.String("Enabled"),
					Filter: &s3.LifecycleRuleFilter{
						Prefix: aws.String(""), // apply to all objects
					},
				},
			},
		},
	})
	if err != nil {
		Config.Logger.Printf("Unable to set lifecycle configuration: %v", err)
		return "", err
	}

	Config.Logger.Printf("DEBUG: Done setting up S3 bucket!")
	return *kmsKeyArn, nil
}

// Function to set up squid and subnets for squid
func setupSquid(hostname string, userName string, cidrstring string, ec2svc *ec2.EC2, vpcid string, igw *string, fwRouteTableId *string, routeTableId *string, instanceType string) (*string, error) {
	_, IPNet, _ := net.ParseCIDR(cidrstring)
	subnet, err := cidr.Subnet(IPNet, 2, 3)
	if err != nil {
		return nil, err
	}
	subnetString := subnet.String()

	// create subnet
	subnetName := fmt.Sprintf("%s-nf-subnet-fw-%s", hostname, userName)
	Config.Logger.Printf("Debug: Creating subnet '%s' with name '%s'", subnet, subnetName)
	subnetId, err := setupSubnet(subnetName, subnetString, vpcid, ec2svc, instanceType)
	if err != nil {
		return nil, err
	}

	// add route to internet gateway
	Config.Logger.Printf("Debug: Creating route to internet '%s' in route table '%s'", *igw, *fwRouteTableId)
	_, err = ec2svc.CreateRoute(&ec2.CreateRouteInput{
		DestinationCidrBlock: aws.String("0.0.0.0/0"),
		GatewayId:            igw,
		RouteTableId:         fwRouteTableId,
	})
	if err != nil {
		return nil, err
	}

	// associate route table to subnet
	_, err = ec2svc.AssociateRouteTable(&ec2.AssociateRouteTableInput{
		RouteTableId: fwRouteTableId,
		SubnetId:     subnetId,
	})
	if err != nil {
		return nil, err
	}
	Config.Logger.Printf("Debug: Associated route table '%s' to subnet '%s'", *fwRouteTableId, *subnetId)

	squidInstanceId, err := launchSquidInstance(hostname, userName, ec2svc, subnetId, vpcid, subnetString, instanceType)
	if err != nil {
		return nil, err
	}

	Config.Logger.Printf("Debug: Will add route to Squid '%s' in route table '%s'", *squidInstanceId, *routeTableId)
	// add or replace route to squid
	_, err = ec2svc.CreateRoute(&ec2.CreateRouteInput{
		DestinationCidrBlock: aws.String("0.0.0.0/0"),
		InstanceId:           squidInstanceId,
		RouteTableId:         routeTableId,
	})

	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			// Note: code `IncorrectInstanceState` should never happen here, because `launchSquidInstance`
			// waits until the instance is ready.
			if aerr.Code() == "RouteAlreadyExists" {
				// the route already exists, replace it
				Config.Logger.Print("Debug: Route already exists, replacing it")
				_, err = ec2svc.ReplaceRoute(&ec2.ReplaceRouteInput{
					DestinationCidrBlock: aws.String("0.0.0.0/0"),
					InstanceId:           squidInstanceId,
					RouteTableId:         routeTableId,
				})
				if err != nil {
					return nil, err
				}
			} else {
				return nil, err
			}
		} else {
			return nil, err
		}
	}

	Config.Logger.Printf("Debug: Created route to Squid '%s' in route table '%s'", *squidInstanceId, *routeTableId)
	return subnetId, nil
}

// Generic function to create subnet, and route table
func setupSubnet(subnetName string, cidr string, vpcid string, ec2Svc *ec2.EC2, instanceType string) (*string, error) {
	// Check if subnet exists if not create it
	exsubnet, err := ec2Svc.DescribeSubnets(&ec2.DescribeSubnetsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("cidr-block"),
				Values: []*string{aws.String(cidr)},
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
	})
	if err != nil {
		return nil, err
	}
	if len(exsubnet.Subnets) > 0 {
		Config.Logger.Printf("Debug: Subnet '%s' already exists, skipping creation", subnetName)
		return exsubnet.Subnets[0].SubnetId, nil
	}

	// Fetch all availability zones
	// this is being limited to a region by the ec2svc that gets passed in.
	describeZonesOutput, err := ec2Svc.DescribeAvailabilityZones(&ec2.DescribeAvailabilityZonesInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to describe availability zones: %v", err)
	}

	// Make sure the selected AZ has the instance type from nextflow configuration available.
	var selectedZone string
	for _, zone := range describeZonesOutput.AvailabilityZones {
		if *zone.State != "available" {
			continue
		}
		result, err := ec2Svc.DescribeInstanceTypeOfferings(&ec2.DescribeInstanceTypeOfferingsInput{
			LocationType: aws.String("availability-zone"),
			Filters: []*ec2.Filter{
				{
					Name:   aws.String("location"),
					Values: []*string{aws.String(*zone.ZoneName)},
				},
				{
					Name:   aws.String("instance-type"),
					Values: []*string{aws.String(instanceType)},
				},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("Error describing instance type offerings: %v", err)
		}
		if len(result.InstanceTypeOfferings) > 0 {
			Config.Logger.Printf("Debug: Zone: %v has instance type %v available. Using that for subnet", *zone.ZoneName, instanceType)
			selectedZone = *zone.ZoneName
			break // Exit the loop if we found a suitable zone
		}
	}

	if selectedZone == "" {
		return nil, fmt.Errorf("no suitable availability zone found")
	}

	// create subnet
	Config.Logger.Printf("Debug: Creating subnet '%v' with name '%s'", cidr, subnetName)
	sn, err := ec2Svc.CreateSubnet(&ec2.CreateSubnetInput{
		CidrBlock:        aws.String(cidr),
		VpcId:            aws.String(vpcid),
		AvailabilityZone: aws.String(selectedZone),
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
	})
	if err != nil {
		return nil, err
	}
	return sn.Subnet.SubnetId, nil
}

func setupRouteTable(hostname string, userName string, ec2svc *ec2.EC2, vpcid string, igwid string, routeTableName string) (*string, error) {
	// Check if route table exists
	exrouteTable, err := ec2svc.DescribeRouteTables(&ec2.DescribeRouteTablesInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String(routeTableName)},
			},
			{
				Name:   aws.String("tag:Environment"),
				Values: []*string{aws.String(os.Getenv("GEN3_ENDPOINT"))},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	if len(exrouteTable.RouteTables) > 0 {
		Config.Logger.Printf("Debug: Route table '%s' already exists, skipping creation", routeTableName)
		return exrouteTable.RouteTables[0].RouteTableId, nil
	}
	routeTable, err := ec2svc.CreateRouteTable(&ec2.CreateRouteTableInput{
		VpcId: &vpcid,
		TagSpecifications: []*ec2.TagSpecification{
			{
				// Name
				ResourceType: aws.String("route-table"),
				Tags: []*ec2.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(routeTableName),
					},
					{
						Key:   aws.String("Environment"),
						Value: aws.String(os.Getenv("GEN3_ENDPOINT")),
					},
				},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	Config.Logger.Printf("Debug: Created route table '%s' with name '%s'", *routeTable.RouteTable.RouteTableId, routeTableName)

	if routeTableName == fmt.Sprintf("%s-nf-fw-rt-%s", hostname, userName) {
		// create route
		Config.Logger.Printf("Debug: Creating route to internet '%s' in route table '%s'", igwid, *routeTable.RouteTable.RouteTableId)
		_, err = ec2svc.CreateRoute(&ec2.CreateRouteInput{
			DestinationCidrBlock: aws.String("0.0.0.0/0"),
			GatewayId:            aws.String(igwid),
			RouteTableId:         routeTable.RouteTable.RouteTableId,
		})
		if err != nil {
			return nil, err
		}
	}
	return routeTable.RouteTable.RouteTableId, nil
}

func associateRouteTablesToSubnets(ec2svc *ec2.EC2, subnets []string, routeTableId string) error {
	// associate route tables to subnets
	for _, subnet := range subnets {
		_, err := ec2svc.AssociateRouteTable(&ec2.AssociateRouteTableInput{
			RouteTableId: aws.String(routeTableId),
			SubnetId:     aws.String(subnet),
		})
		if err != nil {
			return err
		}
		Config.Logger.Printf("Debug: Associated route table '%s' to subnet '%s'", routeTableId, subnet)
	}
	return nil
}

func launchSquidInstance(hostname string, userName string, ec2svc *ec2.EC2, subnetId *string, vpcId string, subnet string, instanceType string) (*string, error) {
	instanceName := fmt.Sprintf("%s-nf-squid-%s", hostname, userName)

	// check if instance already exists, if it does start it
	// Check that the state of existing instance is either stopped,stopping or running
	descInstanceInput := &ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("instance-state-name"),
				Values: []*string{aws.String("stopped"), aws.String("stopping"), aws.String("running"), aws.String("pending")},
			},
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String(instanceName)},
			},
			{
				Name:   aws.String("tag:Environment"),
				Values: []*string{aws.String(os.Getenv("GEN3_ENDPOINT"))},
			},
		},
	}
	exinstance, err := ec2svc.DescribeInstances(descInstanceInput)
	if err != nil {
		return nil, err
	}

	var instanceId string
	if len(exinstance.Reservations) > 0 { // instance already exists
		instanceId = *exinstance.Reservations[0].Instances[0].InstanceId
	} else { // instance does not already exist: create it
		// User data script to install and run Squid
		userData := `#!/bin/bash
USER="ec2-user"
USER_HOME="/home/$USER"
CLOUD_AUTOMATION="$USER_HOME/cloud-automation"
(
cd $USER_HOME
sudo yum update -y
sudo yum install git lsof -y
git clone https://github.com/uc-cdis/cloud-automation.git
cd $CLOUD_AUTOMATION
git pull

chown -R $USER. $CLOUD_AUTOMATION
cd $USER_HOME

# Configure iptables
cp ${CLOUD_AUTOMATION}/flavors/squid_auto/startup_configs/iptables-docker.conf /etc/iptables.conf
cp ${CLOUD_AUTOMATION}/flavors/squid_auto/startup_configs/iptables-rules /etc/network/if-up.d/iptables-rules

chown root: /etc/network/if-up.d/iptables-rules
chmod 0755 /etc/network/if-up.d/iptables-rules

## Enable iptables for NAT. We need this so that the proxy can be used transparently
iptables-restore < /etc/iptables.conf
iptables-save > /etc/sysconfig/iptables

SQUID_CONFIG_DIR="/etc/squid"
SQUID_LOGS_DIR="/var/log/squid"
SQUID_CACHE_DIR="/var/cache/squid"

###############################################################
# Squid configuration files
###############################################################
mkdir -p ${SQUID_CONFIG_DIR}/ssl
cp ${CLOUD_AUTOMATION}/files/squid_whitelist/ftp_whitelist ${SQUID_CONFIG_DIR}/ftp_whitelist
cp ${CLOUD_AUTOMATION}/files/squid_whitelist/web_whitelist ${SQUID_CONFIG_DIR}/web_whitelist
cp ${CLOUD_AUTOMATION}/files/squid_whitelist/web_wildcard_whitelist ${SQUID_CONFIG_DIR}/web_wildcard_whitelist
cp ${CLOUD_AUTOMATION}/flavors/squid_auto/startup_configs/squid.conf ${SQUID_CONFIG_DIR}/squid.conf
cp ${CLOUD_AUTOMATION}/flavors/squid_auto/startup_configs/cachemgr.conf ${SQUID_CONFIG_DIR}/cachemgr.conf
cp ${CLOUD_AUTOMATION}/flavors/squid_auto/startup_configs/errorpage.css ${SQUID_CONFIG_DIR}/errorpage.css
cp ${CLOUD_AUTOMATION}/flavors/squid_auto/startup_configs/mime.conf ${SQUID_CONFIG_DIR}/mime.conf
// use a sed command to replace pid_filename xxxx to pid_filename none
sed -i 's/pid_filename .*/pid_filename none/g' ${SQUID_CONFIG_DIR}/squid.conf


#####################
# for HTTPS
#####################
openssl genrsa -out ${SQUID_CONFIG_DIR}/ssl/squid.key 2048
openssl req -new -key ${SQUID_CONFIG_DIR}/ssl/squid.key -out ${SQUID_CONFIG_DIR}/ssl/squid.csr -subj '/C=XX/ST=XX/L=squid/O=squid/CN=squid'
openssl x509 -req -days 3650 -in ${SQUID_CONFIG_DIR}/ssl/squid.csr -signkey ${SQUID_CONFIG_DIR}/ssl/squid.key -out ${SQUID_CONFIG_DIR}/ssl/squid.crt
cat ${SQUID_CONFIG_DIR}/ssl/squid.key ${SQUID_CONFIG_DIR}/ssl/squid.crt | sudo tee ${SQUID_CONFIG_DIR}/ssl/squid.pem
mkdir -p ${SQUID_LOGS_DIR} ${SQUID_CACHE_DIR}
chown -R nobody:nogroup ${SQUID_LOGS_DIR} ${SQUID_CACHE_DIR} ${SQUID_CONFIG_DIR}

systemctl restart docker
$(command -v docker) run --name squid --restart=always --network=host -d \
	--volume ${SQUID_LOGS_DIR}:${SQUID_LOGS_DIR} \
	--volume ${SQUID_CACHE_DIR}:${SQUID_CACHE_DIR} \
	--volume ${SQUID_CONFIG_DIR}:${SQUID_CONFIG_DIR}:ro \
	quay.io/cdis/squid:master


) > /var/log/bootstrapping_script.log`

		// Set private IP to be the 10th ip in subnet range
		_, ipnet, _ := net.ParseCIDR(subnet)
		privateIP := ipnet.IP
		privateIP[3] += 10
		Config.Logger.Print("Debug: Private IP: ", privateIP.String())

		// Get the latest amazonlinux AMI
		amiId, err := getLatestAmazonLinuxAmi(ec2svc)
		if err != nil {
			return nil, err
		}

		sgId, err := setupFwSecurityGroup(hostname, userName, ec2svc, &vpcId)
		if err != nil {
			return nil, err
		}

		// Launch EC2 instance
		squid, err := ec2svc.RunInstances(&ec2.RunInstancesInput{
			ImageId:      amiId,
			InstanceType: aws.String(instanceType),
			MinCount:     aws.Int64(1),
			MaxCount:     aws.Int64(1),
			// Network interfaces
			NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
				{
					AssociatePublicIpAddress: aws.Bool(true),
					DeviceIndex:              aws.Int64(0),
					DeleteOnTermination:      aws.Bool(true),
					SubnetId:                 subnetId,
					Groups:                   []*string{sgId},
				},
			},
			// base64 encoded user data script
			UserData: aws.String(base64.StdEncoding.EncodeToString([]byte(userData))),
			TagSpecifications: []*ec2.TagSpecification{
				{
					ResourceType: aws.String("instance"),
					Tags: []*ec2.Tag{
						{
							Key:   aws.String("Name"),
							Value: aws.String(instanceName),
						},
						{
							Key:   aws.String("Environment"),
							Value: aws.String(os.Getenv("GEN3_ENDPOINT")),
						},
					},
				},
			},
		})
		if err != nil {
			Config.Logger.Print("Error launching instance: ", err)
			return nil, err
		}

		// make sure the eni has source/destionation check disabled
		// https://docs.aws.amazon.com/vpc/latest/userguide/VPC_NAT_Instance.html#EIP_Disable_SrcDestCheck
		_, err = ec2svc.ModifyNetworkInterfaceAttribute(&ec2.ModifyNetworkInterfaceAttributeInput{
			NetworkInterfaceId: squid.Instances[0].NetworkInterfaces[0].NetworkInterfaceId,
			SourceDestCheck: &ec2.AttributeBooleanValue{
				Value: aws.Bool(false),
			},
		})
		if err != nil {
			return nil, err
		}

		Config.Logger.Print("Debug: Launched Squid instance")
		instanceId = *squid.Instances[0].InstanceId
	}

	// Wait until the instance is running
	maxIter := 6
	iterDelaySecs := 10
	var instanceState string
	for i := 0; ; i++ {
		exinstance, err = ec2svc.DescribeInstances(descInstanceInput)
		if err != nil {
			return nil, err
		}
		instanceState = *exinstance.Reservations[0].Instances[0].State.Name
		if instanceState == "running" {
			Config.Logger.Print("Debug: Squid instance is ready")
			break
		}
		if instanceState == "stopped" {
			Config.Logger.Print("Debug: Instance already exists and is stopped, starting it now")
			_, err := ec2svc.StartInstances(&ec2.StartInstancesInput{
				InstanceIds: []*string{
					&instanceId,
				},
			})
			if err != nil {
				return nil, err
			}
		}
		if i == maxIter {
			return nil, fmt.Errorf("squid instance is not ready after %v seconds. Exiting", maxIter*iterDelaySecs)
		}
		Config.Logger.Printf("Info: Squid instance is %s, waiting %vs and checking again", instanceState, iterDelaySecs)
		time.Sleep(time.Duration(iterDelaySecs) * time.Second)
	}

	return &instanceId, nil
}

func setupFwSecurityGroup(hostname string, userName string, ec2svc *ec2.EC2, vpcId *string) (*string, error) {
	// create security group
	sgName := fmt.Sprintf("%s-nf-sg-fw-%s", hostname, userName)

	// Check if security group exists
	exsecurityGroup, err := ec2svc.DescribeSecurityGroups(&ec2.DescribeSecurityGroupsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("group-name"),
				Values: []*string{aws.String(sgName)},
			},
			{
				Name:   aws.String("vpc-id"),
				Values: []*string{vpcId},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	if len(exsecurityGroup.SecurityGroups) > 0 {
		Config.Logger.Printf("Debug: Security group '%s' already exists, skipping creation", sgName)
		return exsecurityGroup.SecurityGroups[0].GroupId, nil
	}

	sgDesc := "Security group for nextflow Squid"
	sgId, err := ec2svc.CreateSecurityGroup(&ec2.CreateSecurityGroupInput{
		Description: &sgDesc,
		GroupName:   &sgName,
		VpcId:       vpcId,
	})
	if err != nil {
		Config.Logger.Printf("Error creating security group '%s': %v", sgName, err)
		return nil, err
	}

	// Add ingress rules
	_, err = ec2svc.AuthorizeSecurityGroupIngress(&ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: sgId.GroupId,
		IpPermissions: []*ec2.IpPermission{
			{
				FromPort:   aws.Int64(0),
				ToPort:     aws.Int64(65535),
				IpProtocol: aws.String("tcp"),
				IpRanges: []*ec2.IpRange{
					{
						// TODO: make this configurable? (MIDRC-747)
						CidrIp: aws.String("192.168.0.0/16"),
					},
				},
			},
		},
	})
	if err != nil {
		Config.Logger.Print("Error adding ingress rule to security group: ", err)
		return nil, err
	}

	return sgId.GroupId, nil
}

// Get latest amazonlinux ami
func getLatestAmazonLinuxAmi(ec2svc *ec2.EC2) (*string, error) {
	ami, err := ec2svc.DescribeImages(&ec2.DescribeImagesInput{
		Filters: []*ec2.Filter{
			{
				Name: aws.String("name"),
				Values: []*string{
					aws.String("amzn2-ami-ecs-hvm-2.0.*"),
				},
			},
			{
				Name:   aws.String("architecture"),
				Values: []*string{aws.String("x86_64")},
			},
		},
		Owners: []*string{
			aws.String("amazon"),
		},
	})
	if err != nil {
		Config.Logger.Print("Error getting latest amazonlinux AMI: ", err)
		return nil, err
	}

	if len(ami.Images) > 0 {
		latestImage := ami.Images[0]
		latestTimeStamp := time.Unix(0, 0).UTC()

		for _, image := range ami.Images {
			creationTimeStamp, _ := time.Parse(time.RFC3339, *image.CreationDate)
			if creationTimeStamp.After(latestTimeStamp) {
				latestTimeStamp = creationTimeStamp
				latestImage = image
			}
		}

		Config.Logger.Printf("Info: Found latest amazonlinux AMI: '%s'", *latestImage.ImageId)
		return latestImage.ImageId, nil
	}
	return nil, errors.New("no amazonlinux AMI found")
}

func getNextflowInstanceAmi(
	imageBuilderReaderRoleArn string,
	nextflowConfig NextflowConfig,
	imagebuilderListImagePipelineImages func(*imagebuilder.ListImagePipelineImagesInput) (*imagebuilder.ListImagePipelineImagesOutput, error),
) (string, error) {
	/*	The `imagebuilderListImagePipelineImages` parameter should not be provided in production. It allows
		us to test this function by mocking the AWS SDK in the tests.
	*/
	var err error
	ami := nextflowConfig.InstanceAmi
	if ami != "" {
		Config.Logger.Printf("Using configured 'nextflow.instance-ami' '%s' and ignoring 'nextflow.instance-ami-builder-arn'", ami)
	} else if nextflowConfig.InstanceAmiBuilderArn != "" {
		ami, err = getLatestImageBuilderAmi(imageBuilderReaderRoleArn, nextflowConfig.InstanceAmiBuilderArn, imagebuilderListImagePipelineImages)
		if err != nil {
			return "", err
		}
	} else {
		return "", fmt.Errorf("one of 'nextflow.instance-ami' and 'nextflow.instance-ami-builder-arn' must be configured")
	}
	return ami, err
}

func cancelBatchJobsInStatus(
	batchJobQueueName string,
	status string,
	batchSvcListJobs func(*batch.ListJobsInput) (*batch.ListJobsOutput, error),
	batchSvcTerminateJob func(*batch.TerminateJobInput) (*batch.TerminateJobOutput, error),
) error {
	listJobsOutput, err := batchSvcListJobs(&batch.ListJobsInput{
		JobQueue:  &batchJobQueueName,
		JobStatus: aws.String(status),
	})
	if err != nil {
		Config.Logger.Printf("Error listing %s jobs in Batch queue '%s': %v", status, batchJobQueueName, err)
		return err
	}
	runningJobs := listJobsOutput.JobSummaryList
	for listJobsOutput.NextToken != nil { // if the result is paginated, get the rest
		listJobsOutput, err = batchSvcListJobs(&batch.ListJobsInput{
			JobQueue:  &batchJobQueueName,
			JobStatus: aws.String(status),
			NextToken: listJobsOutput.NextToken,
		})
		if err != nil {
			Config.Logger.Printf("Error listing %s jobs in Batch queue '%s': %v", status, batchJobQueueName, err)
			return err
		}
		runningJobs = append(runningJobs, listJobsOutput.JobSummaryList...)
	}
	if len(runningJobs) == 0 {
		Config.Logger.Printf("Debug: No %s jobs to cancel", status)
	}

	// `TerminateJob` cancels jobs in SUBMITTED, PENDING or RUNNABLE state and terminates jobs
	// in STARTING or RUNNING state
	for _, job := range runningJobs {
		Config.Logger.Printf("Canceling %s job: ID '%s', name '%s'", status, *job.JobId, *job.JobName)
		_, err := batchSvcTerminateJob(&batch.TerminateJobInput{
			JobId:  job.JobId,
			Reason: aws.String("User's workspace was terminated"),
		})
		if err != nil {
			Config.Logger.Printf("Error terminating job '%s': %v", *job.JobId, err)
			return err
		}
	}

	return nil
}

// delete the AWS resources created to launch nextflow workflows
func cleanUpNextflowResources(
	userName string,
	iamSvcListAccessKeys func(*iam.ListAccessKeysInput) (*iam.ListAccessKeysOutput, error),
	iamSvcDeleteAccessKey func(*iam.DeleteAccessKeyInput) (*iam.DeleteAccessKeyOutput, error),
	batchSvcListJobs func(*batch.ListJobsInput) (*batch.ListJobsOutput, error),
	batchSvcTerminateJob func(*batch.TerminateJobInput) (*batch.TerminateJobOutput, error),
) error {
	/*	Clean up Nextflow resource when a workspace is terminated:
		- delete IAM user access keys
		- stop Squid instance
		- (S3 bucket cleanup is disabled)
		- cancel any running Batch jobs

		The `iamSvcListAccessKeys`, `iamSvcDeleteAccessKey`, `batchSvcListJobs` and `batchSvcTerminateJob`
		parameters should not be provided in production. They allow us to test this function by mocking
		the AWS SDK in the tests.
	*/
	payModel, err := getCurrentPayModel(userName)
	if err != nil {
		return err
	}

	// credentials and AWS services init
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String("us-east-1"),
	}))
	awsAccountId, awsConfig, err := getNextflowAwsSettings(sess, payModel, userName, "deleting")
	if err != nil {
		return err
	}
	Config.Logger.Printf("Debug: AWS account ID: '%v'", awsAccountId)
	iamSvc := iam.New(sess, &awsConfig)
	ec2Svc := ec2.New(sess, &awsConfig)
	batchSvc := batch.New(sess, &awsConfig)

	userName = escapism(userName)
	hostname := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-")

	// delete the user's access keys
	nextflowUserName := fmt.Sprintf("%s-nf-%s", hostname, userName)
	if iamSvcListAccessKeys == nil {
		iamSvcListAccessKeys = iamSvc.ListAccessKeys
	}
	if iamSvcDeleteAccessKey == nil {
		iamSvcDeleteAccessKey = iamSvc.DeleteAccessKey
	}
	err = deleteUserAccessKeys(nextflowUserName, iamSvcListAccessKeys, iamSvcDeleteAccessKey)
	if err != nil {
		Config.Logger.Printf("Unable to delete access keys for user '%s': %v", nextflowUserName, err)
		return err
	}

	err = stopSquidInstance(hostname, userName, ec2Svc)
	if err != nil {
		Config.Logger.Printf("Warning: Unable to stop Squid instance - continuing: %v", err)
	}

	// NOTE: This was disabled because researchers may need to keep the intermediary files. Instead of
	// deleting, we could set bucket lifecycle rules to delete after X days.
	// NOTE: The code below works locally but not once deployed

	// bucketName = xyz...
	// // delete the user's folder and its contents in the nextflow bucket
	// objectsKey := fmt.Sprintf("%s/", userName)
	// // objectsIter := s3manager.NewDeleteListIterator(s3Svc, &s3.ListObjectsInput{
	// // 	Bucket: &bucketName,
	// // 	Prefix: &objectsKey,
	// // })
	// objectsIter := s3manager.NewDeleteListIterator(s3Svc, &s3.ListObjectsInput{
	// 	Bucket: aws.String("xxx-nf"),
	// 	Prefix: aws.String("xxx-40uchicago-2eedu/"),
	// })
	// if err := s3manager.NewBatchDeleteWithClient(s3Svc).Delete(context.Background(), objectsIter); err != nil {
	// 	Config.Logger.Printf("Unable to delete objects in bucket '%s' at '%s' - continuing: %v", bucketName, objectsKey, err)
	// } else {
	// 	Config.Logger.Printf("Debug: Deleted objects in bucket '%s' at '%s'", bucketName, objectsKey)
	// }

	// cancel any Batch jobs that are still running (or about to run) for this user
	batchJobQueueName := fmt.Sprintf("%s-nf-job-queue-%s", hostname, userName)
	if batchSvcListJobs == nil {
		batchSvcListJobs = batchSvc.ListJobs
	}
	if batchSvcTerminateJob == nil {
		batchSvcTerminateJob = batchSvc.TerminateJob
	}
	// First cancel jobs that are already incurring cost ("running" status). Then cancel in the order a job goes
	// through statuses (first submitted, then pending, etc) to ensure all jobs are deleted. Finally cancel any
	// jobs that reached the "running" status in the meantime. Ignore jobs in "succeeded" or "failed" status.
	statusToCancel := []string{batch.JobStatusRunning, batch.JobStatusSubmitted, batch.JobStatusPending, batch.JobStatusRunnable, batch.JobStatusStarting, batch.JobStatusRunning}
	Config.Logger.Printf("Canceling user's jobs in Batch queue '%s'...", batchJobQueueName)
	for _, status := range statusToCancel {
		err = cancelBatchJobsInStatus(batchJobQueueName, status, batchSvcListJobs, batchSvcTerminateJob)
		if err != nil {
			Config.Logger.Printf("Error canceling user's Batch jobs: %v", err)
			return err
		}
	}

	return nil
}

func deleteUserAccessKeys(
	nextflowUserName string,
	iamSvcListAccessKeys func(*iam.ListAccessKeysInput) (*iam.ListAccessKeysOutput, error),
	iamSvcDeleteAccessKey func(*iam.DeleteAccessKeyInput) (*iam.DeleteAccessKeyOutput, error),
) error {
	listAccessKeysResult, err := iamSvcListAccessKeys(&iam.ListAccessKeysInput{
		UserName: &nextflowUserName,
	})
	if err != nil {
		Config.Logger.Printf("Unable to list access keys for user '%s': %v", nextflowUserName, err)
		return err
	}
	for _, key := range listAccessKeysResult.AccessKeyMetadata {
		Config.Logger.Printf("Deleting access key '%s' for user '%s'", *key.AccessKeyId, nextflowUserName)
		_, err := iamSvcDeleteAccessKey(&iam.DeleteAccessKeyInput{
			UserName:    &nextflowUserName,
			AccessKeyId: key.AccessKeyId,
		})
		if err != nil {
			Config.Logger.Printf("Warning: Unable to delete access key '%s' for user '%s' - continuing: %v", *key.AccessKeyId, nextflowUserName, err)
		}
	}
	Config.Logger.Printf("Debug: Deleted all access keys for Nextflow AWS user '%s'", nextflowUserName)
	return nil
}

var stopSquidInstance = func(hostname string, userName string, ec2svc *ec2.EC2) error {
	// check if instance already exists, if it does stop it and return
	exinstance, err := ec2svc.DescribeInstances(&ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("instance-state-name"),
				Values: []*string{aws.String("stopped"), aws.String("stopping"), aws.String("running"), aws.String("pending")},
			},
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String(fmt.Sprintf("%s-nf-squid-%s", hostname, userName))},
			},
			{
				Name:   aws.String("tag:Environment"),
				Values: []*string{aws.String(os.Getenv("GEN3_ENDPOINT"))},
			},
		},
	})
	if err != nil {
		return err
	}
	if len(exinstance.Reservations) > 0 {
		// Make sure the instance is stopped
		if *exinstance.Reservations[0].Instances[0].State.Name == "stopped" {
			Config.Logger.Print("Debug: Squid instance already stopped, skipping")
			return nil
		}

		// Terminate the instance
		Config.Logger.Print("Debug: running Squid instance found, terminating it now")
		_, err := ec2svc.TerminateInstances(&ec2.TerminateInstancesInput{
			InstanceIds: []*string{
				exinstance.Reservations[0].Instances[0].InstanceId,
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

var generateNextflowConfig = func(nextflowGlobalConfig NextflowGlobalConfig, userName string) (string, error) {
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String("us-east-1"),
	}))
	payModel, err := getCurrentPayModel(userName)
	if err != nil {
		return "", err
	}
	awsAccountId, awsConfig, err := getNextflowAwsSettings(sess, payModel, userName, "fetching")
	if err != nil {
		return "", err
	}

	// get the queue name
	userName = escapism(userName)
	hostname := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-")
	batchJobQueueName := fmt.Sprintf("%s-nf-job-queue-%s", hostname, userName)

	// get the work dir
	bucketName := fmt.Sprintf("%s-nf-%s", hostname, awsAccountId)
	workDir := fmt.Sprintf("s3://%s/%s", bucketName, userName)

	// get the jobs role
	tag := fmt.Sprintf("%s-hatchery-nf-%s", hostname, userName)
	pathPrefix := aws.String(fmt.Sprintf("/%s/", tag))
	iamSvc := iam.New(sess, &awsConfig)
	listRolesResult, err := iamSvc.ListRoles(&iam.ListRolesInput{
		PathPrefix: pathPrefix,
	})
	if err != nil || len(listRolesResult.Roles) == 0 {
		Config.Logger.Printf("Error getting role with path prefix '%s', which should already exist: %v", *pathPrefix, err)
		return "", err
	}
	nextflowJobsRoleArn := *listRolesResult.Roles[0].Arn

	Config.Logger.Printf("Generating Nextflow configuration with: Batch queue: '%s'. Job role: '%s'. Workdir: '%s'.", batchJobQueueName, nextflowJobsRoleArn, workDir)

	configContents := fmt.Sprintf(
		`plugins {
	id 'nf-amazon'
}
process {
	executor = 'awsbatch'
	queue = '%s'
	container = '%s'
}
aws {
	batch {
		cliPath = '/home/ec2-user/miniconda/bin/aws'
		jobRole = '%s'
	}
}
workDir = '%s'`,
		batchJobQueueName,
		nextflowGlobalConfig.SampleConfigPublicImage,
		nextflowJobsRoleArn,
		workDir,
	)

	return configContents, nil
}

func replaceAllUsernamePlaceholders(strArray []string, userName string) []string {
	var result []string
	for _, str := range strArray {
		result = append(result, strings.Replace(str, "{{username}}", userName, -1))
	}
	return result
}

// function to generate user data
func generateEcrLoginUserData(jobImageWhitelist []string, userName string) string {
	var ecrRepos []string
	for _, image := range replaceAllUsernamePlaceholders(jobImageWhitelist, userName) {
		if strings.Contains(image, ".ecr.") {
			// NOTE: on the ECR side, tags are ignored and users are allowed access to the whole repo.
			repo := strings.Split(image, ":")[0]
			ecrRepos = append(ecrRepos, repo)
		}
	}

	// TODO: read region from config (MIDRC-743)
	runCmd := ""
	for _, approvedRepo := range ecrRepos {
		runCmd += fmt.Sprintf(`
- aws ecr get-login-password --region us-east-1 | docker login --username AWS --password-stdin %s`, approvedRepo)
	}

	userData := fmt.Sprintf(`MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="==MYBOUNDARY=="

--==MYBOUNDARY==
Content-Type: text/cloud-config; charset="us-ascii"

packages:
- aws-cli
runcmd:%s
--==MYBOUNDARY==--`, runCmd)

	return base64.StdEncoding.EncodeToString([]byte(userData))
}
