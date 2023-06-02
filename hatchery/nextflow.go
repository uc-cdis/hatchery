package hatchery

import (
	"fmt"
	// "os" // TODO remove
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials" // TODO remove
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/batch"
	"github.com/aws/aws-sdk-go/service/iam"
)


// create the per-user AWS resources required to launch nextflow workflows
func createNextflowUserResources(userName string) (string, string, error) { // TODO move to a different file
	// TODO are the resources shared between all QA envs / between staging and prod if they are in
	// the same account? Add the env to all the names?

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

	// set the tags we will use on all created resources
	// batch and iam accept different formats
	tag := fmt.Sprintf("hatchery-nextflow-%s", userName)
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

	// create AWS batch job queue
	batchJobQueueName := fmt.Sprintf("nextflow-job-queue-%s", userName)
	_, err := batchSvc.CreateJobQueue(&batch.CreateJobQueueInput{
		JobQueueName: &batchJobQueueName,
		ComputeEnvironmentOrder: []*batch.ComputeEnvironmentOrder{
			{
				ComputeEnvironment: aws.String("arn:aws:batch:us-east-1:707767160287:compute-environment/nextflow-pauline-compute-env"), // TODO update
				Order: aws.Int64(int64(0)),
			},
		},
		Priority: aws.Int64(int64(0)),
		Tags: tagsMap,
	})
	if err != nil {
		if strings.Contains(err.Error(), "Object already exists") {
			Config.Logger.Printf("Debug: AWS Batch job queue '%s' already exists", batchJobQueueName)
		} else {
			Config.Logger.Printf("Error creating AWS Batch job queue '%s': %v", batchJobQueueName, err)
			return "", "", err
		}
	} else {
		Config.Logger.Printf("Created AWS Batch job queue '%s'", batchJobQueueName)
	}

	// create IAM policy for nextflow-created jobs
	policyName := fmt.Sprintf("nextflow-jobs-%s", userName)
	nextflowJobsPolicyArn, err := createPolicyIfNotExist(iamSvc, policyName, pathPrefix, tags, aws.String(fmt.Sprintf(`{
		"Version": "2012-10-17",
		"Statement": [
			{
				"Effect": "Allow",
				"Action": [
					"s3:*"
				],
				"Resource": [
					"arn:aws:s3:::nextflow-ctds",
					"arn:aws:s3:::nextflow-ctds/%s/*"
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
	}`, userName)))
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

	// create policy to role for nextflow-created jobs
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
	policyName = fmt.Sprintf("nextflow-%s", userName)
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
					"arn:aws:s3:::nextflow-ctds",
					"arn:aws:s3:::nextflow-ctds/%s/*"
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
	}`, batchJobQueueName, userName, nextflowJobsRoleArn)))
	if err != nil {
		return "", "", err
	}

	// create user for nextflow client
	nextflowUserName := fmt.Sprintf("nextflow-%s", userName)
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

	// create policy to user for nextflow client
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

	// TODO do this at pod termination instead
	listAccessKeysResult, err := iamSvc.ListAccessKeys(&iam.ListAccessKeysInput{
		UserName: &nextflowUserName,
	})
	if err != nil {
		Config.Logger.Printf("Unable to list access keys for user '%s': %v", nextflowUserName, err)
		return "", "", err
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
