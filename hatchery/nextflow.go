package hatchery

import (
	// "context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	// "github.com/aws/aws-sdk-go/aws/credentials" // TODO remove
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/batch"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/iam"
	"github.com/aws/aws-sdk-go/service/s3"
	// "github.com/aws/aws-sdk-go/service/s3/s3manager"
)



// create the global AWS resources required to launch nextflow workflows
func CreateNextflowGlobalResources() (string, string, error) {
	// sess := session.Must(session.NewSessionWithOptions(session.Options{
	// 	Config: aws.Config{
	// 		Region: aws.String("us-east-1"),
	// 	},
	// }))
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region: aws.String("us-east-1"),
			// Credentials: credentials.NewStaticCredentials(os.Getenv("AccessKeyId"), os.Getenv("SecretAccessKey"), ""),
		},
	}))
	batchSvc := batch.New(sess)
	ec2Svc := ec2.New(sess)
	s3Svc := s3.New(sess)

	hostname := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-")

	// set the tags we will use on all created resources
	tag := fmt.Sprintf("%s-hatchery-nf", hostname)
	tagsMap := map[string]*string{
		"name": &tag,
	}

	Config.Logger.Printf("Getting AWS account ID...")
	awsAccountId, err := getAwsAccountId(sess)
	if err != nil {
		Config.Logger.Printf("Error getting AWS account ID: %v", err)
		return "", "", err
	}
	Config.Logger.Printf("AWS account ID: %v", awsAccountId)

	Config.Logger.Printf("Getting default subnets...")
	subnetsResult, err := ec2Svc.DescribeSubnets(&ec2.DescribeSubnetsInput{
		Filters: []*ec2.Filter{
			{
				Name: aws.String("default-for-az"),
				Values: []*string{
					aws.String("true"),
				},
			},
		},
	})
	if err != nil {
		Config.Logger.Printf("Error getting default subnets: %v", err)
		return "", "", err
	}
	// select the 1st returned subnet
	subnetId := *subnetsResult.Subnets[0].SubnetId

	batchComputeEnvName := fmt.Sprintf("%s-nf-compute-env", hostname)
	batchComputeEnvResult, err := batchSvc.CreateComputeEnvironment(&batch.CreateComputeEnvironmentInput{
		ComputeEnvironmentName: &batchComputeEnvName,
		Type: aws.String("MANAGED"), // TODO maybe using unmanaged allows users to choose the instance types? or does nextflow control that?
		ComputeResources: &batch.ComputeResource{
			Ec2Configuration: []*batch.Ec2Configuration{
				{
					ImageIdOverride: aws.String("ami-0069809e4eba54531"), //aws.String(nextflowConfig.InstanceAMI),
					ImageType: aws.String("ECS_AL2"),
				},
			},
			InstanceRole: aws.String(fmt.Sprintf("arn:aws:iam::%s:instance-profile/ecsInstanceRole", awsAccountId)),
			AllocationStrategy: aws.String("BEST_FIT_PROGRESSIVE"),
			MinvCpus: aws.Int64(int64(0)), //aws.Int64(int64(nextflowConfig.InstanceMinVCpus)),
			MaxvCpus: aws.Int64(int64(9)), //aws.Int64(int64(nextflowConfig.InstanceMaxVCpus)),
			InstanceTypes: []*string{aws.String("optimal")},
			SecurityGroupIds: []*string{aws.String("sg-adf1bedf")}, // TODO
			Subnets: []*string{&subnetId},
			Type: aws.String("SPOT"), //aws.String(nextflowConfig.InstanceType),
			Tags: tagsMap,
		},
		Tags: tagsMap,
	})
	batchComputeEnvArn := ""
	if err != nil {
		if strings.Contains(err.Error(), "Object already exists") {
			Config.Logger.Printf("Debug: Batch compute environment '%s' already exists", batchComputeEnvName)
			listComputeEnvsResult, err := batchSvc.DescribeComputeEnvironments(&batch.DescribeComputeEnvironmentsInput{
				ComputeEnvironments: []*string{
					&batchComputeEnvName,
				},
			})
			if err != nil {
				Config.Logger.Printf("Error getting existing compute environment '%s': %v", batchComputeEnvName, err)
				return "", "", err
			}
			batchComputeEnvArn = *listComputeEnvsResult.ComputeEnvironments[0].ComputeEnvironmentArn
		} else {
			Config.Logger.Printf("Error creating Batch compute environment '%s': %v", batchComputeEnvName, err)
			return "", "", err
		}
	} else {
		Config.Logger.Printf("Created Batch compute environment '%s'", batchComputeEnvName)
		batchComputeEnvArn = *batchComputeEnvResult.ComputeEnvironmentArn
	}

	bucketName := fmt.Sprintf("%s-nf", hostname)
	_, err = s3Svc.CreateBucket(&s3.CreateBucketInput{
		Bucket: &bucketName,
		// TODO conditional LocationConstraint? this only works if not "us-east-1"?
		// CreateBucketConfiguration: &s3.CreateBucketConfiguration{
		// 	LocationConstraint: aws.String("us-east-1"),
		// },
	})
	if err != nil {
		Config.Logger.Printf("Error creating S3 bucket '%s': %v", bucketName, err)
		return "", "", err
	} else {
		Config.Logger.Printf("Created S3 bucket '%s'", bucketName)
	}

	return bucketName, batchComputeEnvArn, nil
}


// create the per-user AWS resources required to launch nextflow workflows
func createNextflowUserResources(userName string, nextflowConfig NextflowConfig, bucketName string, batchComputeEnvArn string) (string, string, error) {
	// TODO get this working with paymodels
	// roleARN := "arn:aws:iam::" + payModel.AWSAccountId + ":role/csoc_adminvm"
	// sess := awstrace.WrapSession(session.Must(session.NewSession(&aws.Config{
	// 	Region: aws.String(payModel.Region),
	// })))
	// creds := stscreds.NewCredentials(sess, roleARN)
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region: aws.String("us-east-1"),
			// Credentials: credentials.NewStaticCredentials(os.Getenv("AccessKeyId"), os.Getenv("SecretAccessKey"), ""),
		},
	}))
	batchSvc := batch.New(sess)
	iamSvc := iam.New(sess)

	userName = escapism(userName)
	hostname := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-")

	// set the tags we will use on all created resources
	// batch and iam accept different formats
	tag := fmt.Sprintf("%s-hatchery-nf-%s", hostname, userName)
	tagsMap := map[string]*string{
		"name": &tag,
	}
	tags := []*iam.Tag{
		&iam.Tag{
			Key: aws.String("name"),
			Value: &tag,
		},
	}
	pathPrefix := aws.String(fmt.Sprintf("/%s/", tag))

	s3BucketWhitelistCondition := "" // if not configured, no buckets are allowed
	if len(nextflowConfig.JobImageWhitelist) > 0 {
		s3BucketWhitelist := ""
		for _, bucket := range nextflowConfig.S3BucketWhitelist{
			Config.Logger.Printf("bucket '%s'", bucket)
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

	// create AWS batch job queue
	batchJobQueueName := fmt.Sprintf("%s-nf-job-queue-%s", hostname, userName)
	_, err := batchSvc.CreateJobQueue(&batch.CreateJobQueueInput{
		JobQueueName: &batchJobQueueName,
		ComputeEnvironmentOrder: []*batch.ComputeEnvironmentOrder{
			{
				ComputeEnvironment: &batchComputeEnvArn,
				Order: aws.Int64(int64(0)),
			},
		},
		Priority: aws.Int64(int64(0)),
		Tags: tagsMap,
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
	nextflowJobsPolicyArn, err := createOrUpdatePolicy(iamSvc, policyName, pathPrefix, tags, aws.String(fmt.Sprintf(`{
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
			}
			%s
		]
	}`, bucketName, userName, bucketName, userName, s3BucketWhitelistCondition)))
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
		RoleName: &roleName,
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
		jobImageWhitelist := fmt.Sprintf(`"%v"`, strings.Join(nextflowConfig.JobImageWhitelist, "\", \""))
		jobImageWhitelistCondition = fmt.Sprintf(`,
		"Condition": {
			"StringLike": {
				"batch:Image": [
					%s
				]
			}
		}`, jobImageWhitelist)
	}
	nextflowPolicyArn, err := createOrUpdatePolicy(iamSvc, policyName, pathPrefix, tags, aws.String(fmt.Sprintf(`{
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
					"batch:TerminateJob"
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
			}
			%s
		]
	}`, nextflowJobsRoleArn, batchJobQueueName, jobImageWhitelistCondition, bucketName, userName, bucketName, userName, s3BucketWhitelistCondition)))
	if err != nil {
		return "", "", err
	}

	// create user for nextflow client
	nextflowUserName := fmt.Sprintf("%s-nf-%s", hostname, userName)
	_, err = iamSvc.CreateUser(&iam.CreateUserInput{
		UserName: &nextflowUserName,
		Tags: tags,
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
		UserName: &nextflowUserName,
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
func cleanUpNextflowUserResources(userName string, bucketName string) (error) {
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region: aws.String("us-east-1"),
			// Credentials: credentials.NewStaticCredentials(os.Getenv("AccessKeyId"), os.Getenv("SecretAccessKey"), ""),
		},
	}))
	iamSvc := iam.New(sess)
	// s3Svc := s3.New(sess)

	userName = escapism(userName)
	hostname := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-")

	// delete the user's access key
	// TODO need to do this before starting a container too, to avoid error:
	// `LimitExceeded: Cannot exceed quota for AccessKeysPerUser: 2`
	nextflowUserName := fmt.Sprintf("%s-nf-%s", hostname, userName)
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
			UserName: &nextflowUserName,
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
	// 	Bucket: aws.String("xxx-nf"),
	// 	Prefix: aws.String("xxx-40uchicago-2eedu/"),
	// })
	// if err := s3manager.NewBatchDeleteWithClient(s3Svc).Delete(context.Background(), objectsIter); err != nil {
	// 	Config.Logger.Printf("Unable to delete objects in bucket '%s' at '%s' - continuing: %v", bucketName, objectsKey, err)
	// } else {
	// 	Config.Logger.Printf("Debug: Deleted objects in bucket '%s' at '%s'", bucketName, objectsKey)
	// }

	return nil
}
