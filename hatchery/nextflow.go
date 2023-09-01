package hatchery

import (
	// "context"
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
		Type:                   aws.String("MANAGED"), // TODO maybe using unmanaged allows users to choose the instance types? or does nextflow control that?
		ComputeResources: &batch.ComputeResource{
			Ec2Configuration: []*batch.Ec2Configuration{
				{
					ImageIdOverride: aws.String("ami-0069809e4eba54531"), //aws.String(nextflowConfig.InstanceAMI),
					ImageType:       aws.String("ECS_AL2"),
				},
			},
			InstanceRole:       aws.String(fmt.Sprintf("arn:aws:iam::%s:instance-profile/ecsInstanceRole", awsAccountId)),
			AllocationStrategy: aws.String("BEST_FIT_PROGRESSIVE"),
			MinvCpus:           aws.Int64(int64(0)), //aws.Int64(int64(nextflowConfig.InstanceMinVCpus)),
			MaxvCpus:           aws.Int64(int64(9)), //aws.Int64(int64(nextflowConfig.InstanceMaxVCpus)),
			InstanceTypes:      []*string{aws.String("optimal")},
			SecurityGroupIds:   []*string{aws.String("sg-adf1bedf")}, // TODO
			Subnets:            []*string{&subnetId},
			Type:               aws.String("SPOT"), //aws.String(nextflowConfig.InstanceType),
			Tags:               tagsMap,
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

	payModel, err := getCurrentPayModel(userName)
	if err != nil {
		return "", "", err
	}

	if payModel.Ecs {
		keyId, keySecret, err := createDirectPayAWSResources(payModel, userName)
		if err != nil {
			Config.Logger.Printf("Error creating Direct Pay AWS resources for user '%s': %v", userName, err)
			return "", "", err
		}

		return keyId, keySecret, nil

	} else {

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
				Key:   aws.String("name"),
				Value: &tag,
			},
		}
		pathPrefix := aws.String(fmt.Sprintf("/%s/", tag))

		// create AWS batch job queue
		// NOTE: There is a limit of 50 job queues per AWS account. If we have more than 50 total nextflow
		// users this call will fail. A solution is to delete unused job queues, but we would still be
		// limited to 50 concurrent nextflow users in the same account.
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
			},
			{
				"Sid": "AllowWhitelistedBuckets",
				"Effect": "Allow",
				"Action": [
					"s3:GetObject",
					"s3:ListBucket"
				],
				"Resource": [
					"arn:aws:s3:::ngi-igenomes",
					"arn:aws:s3:::ngi-igenomes/*"
				]
			}
		]
	}`, bucketName, userName, bucketName, userName)))
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
		/* Notes:
		- `batch:DescribeComputeEnvironments` is listed as required in the Nextflow docs, but it
		works fine without it.
		- `batch:DescribeJobs` and `batch:DescribeJobDefinitions` do not support granular authz,
		and "*" allows users to see all the jobs / job definitions in the account. This is acceptable
		here because Nextflow workflows should only be deployed in the user's own AWS account
		(direct-pay-only workspace).
		- Access to whitelisted public buckets such as `s3://ngi-igenomes` can be added
		TODO make allowed buckets configurable?
		- If you update this policy, you will need to update the logic to update the IAM policy and
		delete previous versions, instead of just continuing if it already exists.
		*/
		policyName = fmt.Sprintf("%s-nf-%s", hostname, userName)
		jobImageCondition := ""
		if len(nextflowConfig.JobImageWhitelist) > 0 {
			jobImageWhitelist := fmt.Sprintf(`"%v"`, strings.Join(nextflowConfig.JobImageWhitelist, "\", \""))
			jobImageCondition = fmt.Sprintf(`,
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
				]%s
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
				"Sid": "AllowWhitelistedBuckets",
				"Effect": "Allow",
				"Action": [
					"s3:GetObject",
					"s3:ListBucket"
				],
				"Resource": [
					"arn:aws:s3:::ngi-igenomes",
					"arn:aws:s3:::ngi-igenomes/*"
				]
			}
		]
	}`, nextflowJobsRoleArn, batchJobQueueName, jobImageCondition, bucketName, userName, bucketName, userName)))
		if err != nil {
			return "", "", err
		}

		// create user for nextflow client
		nextflowUserName := fmt.Sprintf("%s-nf-%s", hostname, userName)
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
}

// delete the per-user AWS resources created to launch nextflow workflows
func cleanUpNextflowUserResources(userName string, bucketName string) error {

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

// create directpay resources
func createDirectPayAWSResources(payModel *PayModel, userName string) (string, string, error) {
	// TODO
	// Create nextflow compute environment if it does not exist
	batchComputeEnvArn, err := setupBatchComputeEnvironment(userName)
	if err != nil {
		// error log
		Config.Logger.Printf("Error creating compute environment for user %s: %s", userName, err.Error())
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
				ComputeEnvironment: batchComputeEnvArn,
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

// clean up direct pay nextflow resources
func cleanUpDirectPayAWSResources(userName string) error {

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
	ec2Svc := ec2.New(sess, &awsConfig)
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

	err = stopSquidInstance(ec2Svc)
	if err != nil {
		Config.Logger.Printf("Warning: Unable to stop SQUID instance - continuing: %v", err)
	}
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
		subnetName := fmt.Sprintf("nextflow-subnet-%d", i)
		Config.Logger.Print("Debug: Creating subnet: ", subnet, " with name: ", subnetName)

		subnetId, err := subnetSetup(subnetName, subnet, vpcid, ec2Svc)
		if err != nil {
			return nil, nil, err
		}

		subnetIds = append(subnetIds, *subnetId)
		Config.Logger.Print("Debug: Created subnet: ", subnetName)
	}

	// setup route table for regular subnets
	routeTableId, err := setupRouteTables(ec2Svc, vpcid, *igw, "nextflow-rt")
	if err != nil {
		return nil, nil, err
	}

	// setup route table for SQUID subnet
	fwRouteTableId, err := setupRouteTables(ec2Svc, vpcid, *igw, "nextflow-fw-rt")
	if err != nil {
		return nil, nil, err
	}

	// associate subnets with route table
	err = associateRouteTablesToSubnets(ec2Svc, subnetIds, *routeTableId)
	if err != nil {
		return nil, nil, err
	}

	// setup SQUID
	fwSubnetId, err := setupSquid(cidrstring, ec2Svc, vpcid, igw, fwRouteTableId, routeTableId)
	if err != nil {
		return nil, nil, err
	}
	Config.Logger.Print("Debug: Created SQUID: ", &fwSubnetId)

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
					// WARNING!
					// TODO: THIS IS A TEST AMI SET UP IN AWS ACCOUNT # 215849870054 FOR TESTING ONLY.
					// TODO: NEED TO MAKE THIS CONFIGURABLE, AND STILL SETUP A FIPS COMPATIBLE PIPELINE
					ImageIdOverride: aws.String("ami-03392f075059ae3ba"), // TODO generate dynamically or get from config
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

// Function to set up squid and subnets for squid
func setupSquid(cidrstring string, svc *ec2.EC2, vpcID string, igw *string, fwRouteTableId *string, routeTableId *string) (*string, error) {
	_, IPNet, _ := net.ParseCIDR(cidrstring)
	subnet, err := cidr.Subnet(IPNet, 2, 3)
	if err != nil {
		return nil, err
	}
	subnetString := subnet.String()

	// create subnet
	subnetName := "nextflow-subnet-fw"
	Config.Logger.Print("Debug: Creating subnet: ", subnet, " with name: ", subnetName)

	subnetId, err := subnetSetup(subnetName, subnetString, vpcID, svc)
	if err != nil {
		return nil, err
	}

	// add route to internet gateway
	_, err = svc.CreateRoute(&ec2.CreateRouteInput{
		DestinationCidrBlock: aws.String("0.0.0.0/0"),
		GatewayId:            igw,
		RouteTableId:         fwRouteTableId,
	})
	if err != nil {
		return nil, err
	}
	Config.Logger.Print("Debug: Created route to internet: ", igw, " in route table: ", fwRouteTableId)

	// associate route table to subnet
	_, err = svc.AssociateRouteTable(&ec2.AssociateRouteTableInput{
		RouteTableId: fwRouteTableId,
		SubnetId:     subnetId,
	})
	if err != nil {
		return nil, err
	}
	Config.Logger.Print("Debug: Associated route table: ", *fwRouteTableId, " to subnet: ", *subnetId)

	// launch squid
	squidInstanceId, err := launchSquidInstance(svc, subnetId, vpcID, subnetString)
	if err != nil {
		return nil, err
	}

	Config.Logger.Print("Will add route to squid: ", *squidInstanceId, " in route table: ", routeTableId)
	// add or replace route to squid
	_, err = svc.CreateRoute(&ec2.CreateRouteInput{
		DestinationCidrBlock: aws.String("0.0.0.0/0"),
		InstanceId:           squidInstanceId,
		RouteTableId:         routeTableId,
	})
	if err != nil {
		// check if route already exists
		if aerr, ok := err.(awserr.Error); ok {
			// handle IncorrectInstanceState error
			if aerr.Code() == "IncorrectInstanceState" {
				Config.Logger.Print("Debug: Need to wait a little before adding route...")
				time.Sleep(10 * time.Second)
				_, err = svc.CreateRoute(&ec2.CreateRouteInput{
					DestinationCidrBlock: aws.String("0.0.0.0/0"),
					InstanceId:           squidInstanceId,
					RouteTableId:         routeTableId,
				})
				if err != nil {
					if aerr, ok := err.(awserr.Error); ok {
						if aerr.Code() == "RouteAlreadyExists" {
							Config.Logger.Print("Debug: Route already exists, replacing it")
							_, err = svc.ReplaceRoute(&ec2.ReplaceRouteInput{
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
					}
					return nil, err
				}
			}

			// if route already exists replace it
			if aerr.Code() == "RouteAlreadyExists" {
				Config.Logger.Print("Debug: Route already exists, replacing it")
				_, err = svc.ReplaceRoute(&ec2.ReplaceRouteInput{
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
	Config.Logger.Print("Debug: Created route to squid: ", *squidInstanceId, " in route table: ", routeTableId)

	return subnetId, nil
}

// Generic function to create subnet, and route table
func subnetSetup(subnetName string, cidr string, vpcid string, ec2Svc *ec2.EC2) (*string, error) {
	// Check if subnet exists if not create it
	descSubnetInput := &ec2.DescribeSubnetsInput{
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
	}
	exsubnet, err := ec2Svc.DescribeSubnets(descSubnetInput)
	if err != nil {
		return nil, err
	}
	if len(exsubnet.Subnets) > 0 {
		Config.Logger.Print("Debug: Subnet already exists, skipping creation")
		return exsubnet.Subnets[0].SubnetId, nil
	}

	// create subnet
	Config.Logger.Print("Debug: Creating subnet: ", cidr, " with name: ", subnetName)
	createSubnetInput := &ec2.CreateSubnetInput{
		CidrBlock: aws.String(cidr),
		VpcId:     aws.String(vpcid),
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
	sn, err := ec2Svc.CreateSubnet(createSubnetInput)
	if err != nil {
		return nil, err
	}
	Config.Logger.Print("Debug: Created subnet: ", sn.Subnet.SubnetId)
	return sn.Subnet.SubnetId, nil
}

func setupRouteTables(svc *ec2.EC2, vpcid string, igwid string, routeTableName string) (*string, error) {
	// Check if route table exists
	descRouteTableInput := &ec2.DescribeRouteTablesInput{
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
	}

	exrouteTable, err := svc.DescribeRouteTables(descRouteTableInput)
	if err != nil {
		return nil, err
	}

	if len(exrouteTable.RouteTables) > 0 {
		Config.Logger.Print("Debug: Route table already exists, skipping creation")
		return exrouteTable.RouteTables[0].RouteTableId, nil
	}
	createRouteTableInput := &ec2.CreateRouteTableInput{
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
	}
	routeTable, err := svc.CreateRouteTable(createRouteTableInput)
	if err != nil {
		return nil, err
	}
	Config.Logger.Print("Debug: Created route table: ", routeTable.RouteTable.RouteTableId)

	if routeTableName == "nextflow-rt-fw" {
		// create route
		_, err = svc.CreateRoute(&ec2.CreateRouteInput{
			DestinationCidrBlock: aws.String("0.0.0.0/0"),
			GatewayId:            aws.String(igwid),
			RouteTableId:         routeTable.RouteTable.RouteTableId,
		})
		if err != nil {
			return nil, err
		}
		Config.Logger.Print("Debug: Created route to internet: ", igwid, " in route table: ", routeTable.RouteTable.RouteTableId)
	}
	return routeTable.RouteTable.RouteTableId, nil
}

func associateRouteTablesToSubnets(svc *ec2.EC2, subnets []string, routeTableId string) error {

	// associate route tables to subnets
	for _, subnet := range subnets {
		_, err := svc.AssociateRouteTable(&ec2.AssociateRouteTableInput{
			RouteTableId: aws.String(routeTableId),
			SubnetId:     aws.String(subnet),
		})
		if err != nil {
			return err
		}
		Config.Logger.Print("Debug: Associated route table: ", routeTableId, " to subnet: ", subnet)
	}
	return nil
}

func launchSquidInstance(svc *ec2.EC2, subnetId *string, vpcId string, subnet string) (*string, error) {

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
				Values: []*string{aws.String("nextflow-squid")},
			},
			{
				Name:   aws.String("tag:Environment"),
				Values: []*string{aws.String(os.Getenv("GEN3_ENDPOINT"))},
			},
		},
	}
	exinstance, err := svc.DescribeInstances(descInstanceInput)
	if err != nil {
		return nil, err
	}
	if len(exinstance.Reservations) > 0 {
		// Make sure the instance is running
		if *exinstance.Reservations[0].Instances[0].State.Name == "running" {
			Config.Logger.Print("Debug: Instance already exists and is running, skipping creation")
			return exinstance.Reservations[0].Instances[0].InstanceId, nil
		}

		// do this in a loop
		for {
			// If the instance is stopping or pending, wait for 10 seconds and check again
			if *exinstance.Reservations[0].Instances[0].State.Name == "stopping" || *exinstance.Reservations[0].Instances[0].State.Name == "pending" {
				Config.Logger.Print("Debug: Instance already exists and is stopping or pending, waiting 10 seconds and checking again")
				time.Sleep(10 * time.Second)
				exinstance, err = svc.DescribeInstances(descInstanceInput)
				if err != nil {
					return nil, err
				}
				continue
			}

			// if state is stopped, or running move on
			if *exinstance.Reservations[0].Instances[0].State.Name == "stopped" || *exinstance.Reservations[0].Instances[0].State.Name == "running" {
				break
			}

		}
		// Start the instance
		_, err := svc.StartInstances(&ec2.StartInstancesInput{
			InstanceIds: []*string{
				exinstance.Reservations[0].Instances[0].InstanceId,
			},
		})
		if err != nil {
			return nil, err
		}
		Config.Logger.Print("Debug: Instance already exists, starting it now")
		return exinstance.Reservations[0].Instances[0].InstanceId, nil
	}

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
	amiId, err := amazonLinuxAmi(svc)
	if err != nil {
		return nil, err
	}

	sgId, err := setupFwSecurityGroup(svc, &vpcId)
	if err != nil {
		return nil, err
	}

	// instance type
	// TODO: configurable via hatchery config
	instanceType := "t3.micro"

	// Launch EC2 instance
	squid, err := svc.RunInstances(&ec2.RunInstancesInput{
		// TODO: better handling of AMI
		ImageId:      amiId,
		InstanceType: aws.String(instanceType),
		MinCount:     aws.Int64(1),
		MaxCount:     aws.Int64(1),
		// // Network interfaces
		NetworkInterfaces: []*ec2.InstanceNetworkInterfaceSpecification{
			{
				AssociatePublicIpAddress: aws.Bool(true),
				DeviceIndex:              aws.Int64(0),
				DeleteOnTermination:      aws.Bool(true),
				SubnetId:                 subnetId,
				Groups:                   []*string{sgId},
				// PrivateIpAddress:         aws.String(privateIP.String()),
			},
		},
		KeyName: aws.String("qureshi"),
		// base64 encoded user data script
		UserData: aws.String(base64.StdEncoding.EncodeToString([]byte(userData))),
		// Tag name
		TagSpecifications: []*ec2.TagSpecification{
			{
				// Name
				ResourceType: aws.String("instance"),
				Tags: []*ec2.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String("nextflow-squid"),
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
	_, err = svc.ModifyNetworkInterfaceAttribute(&ec2.ModifyNetworkInterfaceAttributeInput{
		NetworkInterfaceId: squid.Instances[0].NetworkInterfaces[0].NetworkInterfaceId,
		SourceDestCheck: &ec2.AttributeBooleanValue{
			Value: aws.Bool(false),
		},
	})
	if err != nil {
		return nil, err
	}

	Config.Logger.Print("Debug: Launched instance")

	return squid.Instances[0].InstanceId, nil
}

func stopSquidInstance(svc *ec2.EC2) error {
	// check if instance already exists, if it does stop it and return
	descInstanceInput := &ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("instance-state-name"),
				Values: []*string{aws.String("stopped"), aws.String("stopping"), aws.String("running"), aws.String("pending")},
			},
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String("nextflow-squid")},
			},
			{
				Name:   aws.String("tag:Environment"),
				Values: []*string{aws.String(os.Getenv("GEN3_ENDPOINT"))},
			},
		},
	}
	exinstance, err := svc.DescribeInstances(descInstanceInput)
	if err != nil {
		return err
	}
	if len(exinstance.Reservations) > 0 {
		// Make sure the instance is stopped
		if *exinstance.Reservations[0].Instances[0].State.Name == "stopped" {
			Config.Logger.Print("Debug: Instance already stopped, skipping")
			return nil
		}

		// Terminate the instance
		_, err := svc.TerminateInstances(&ec2.TerminateInstancesInput{
			InstanceIds: []*string{
				exinstance.Reservations[0].Instances[0].InstanceId,
			},
		})
		if err != nil {
			return err
		}
		Config.Logger.Print("Debug: running squid instance found, terminating it now")
	}
	return nil
}

func setupFwSecurityGroup(svc *ec2.EC2, vpcId *string) (*string, error) {
	// create security group
	sgName := "nextflow-sg-fw"

	// Check if security group exists
	descSecurityGroupInput := &ec2.DescribeSecurityGroupsInput{
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
	}
	exsecurityGroup, err := svc.DescribeSecurityGroups(descSecurityGroupInput)
	if err != nil {
		return nil, err
	}
	if len(exsecurityGroup.SecurityGroups) > 0 {
		Config.Logger.Print("Debug: Security group already exists, skipping creation")
		return exsecurityGroup.SecurityGroups[0].GroupId, nil
	}

	sgDesc := "Security group for nextflow SQUID"
	sgId, err := svc.CreateSecurityGroup(&ec2.CreateSecurityGroupInput{
		Description: &sgDesc,
		GroupName:   &sgName,
		VpcId:       vpcId,
	})
	if err != nil {
		Config.Logger.Print("Error creating security group: ", err)
		return nil, err
	}

	return sgId.GroupId, nil
}

// Get latest amazonlinux ami
func amazonLinuxAmi(svc *ec2.EC2) (*string, error) {
	ami, err := svc.DescribeImages(&ec2.DescribeImagesInput{
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

		Config.Logger.Print(latestImage)
		return latestImage.ImageId, nil
	}
	return nil, errors.New("No amazonlinux AMI found")
}
