package hatchery

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/iam"
)

func (creds *CREDS) taskRole(userName string) (*string, error) {
	svc := iam.New(session.Must(session.NewSession(&aws.Config{
		Credentials: creds.creds,
		Region:      aws.String("us-east-1"),
	})))

	taskRoleInput := &iam.GetRoleInput{
		RoleName: aws.String(userToResourceName(userName, "pod")),
	}
	taskRole, _ := svc.GetRole(taskRoleInput)
	if taskRole.Role != nil {
		return taskRole.Role.Arn, nil
	} else {
		policyAlreadyExists := false
		policy, err := svc.CreatePolicy(&iam.CreatePolicyInput{
			PolicyDocument: aws.String(`{
				"Version": "2012-10-17",
				"Statement": [
					{
						"Sid": "HatcheryPolicy",
						"Effect": "Allow",
						"Action": "elasticfilesystem:*",
						"Resource": [
							"arn:aws:elasticfilesystem:*:*:access-point/*",
							"arn:aws:elasticfilesystem:*:*:file-system/*"
						]
					}
				]
			}`),
			PolicyName: aws.String(fmt.Sprintf("ws-task-policy-%s", userName)),
		})
		if err != nil {
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				// Update the policy to the latest spec if it is existed already
				case iam.ErrCodeEntityAlreadyExistsException:
					policyAlreadyExists = true
					fmt.Println(iam.ErrCodeEntityAlreadyExistsException, aerr.Error())
				case iam.ErrCodeLimitExceededException:
					fmt.Println(iam.ErrCodeLimitExceededException, aerr.Error())
				case iam.ErrCodeNoSuchEntityException:
					fmt.Println(iam.ErrCodeNoSuchEntityException, aerr.Error())
				case iam.ErrCodeServiceFailureException:
					fmt.Println(iam.ErrCodeServiceFailureException, aerr.Error())
				default:
					fmt.Println(aerr.Error())
				}
			}
			if !policyAlreadyExists {
				return nil, err
			}
		}
		createTaskRoleInput := &iam.CreateRoleInput{
			RoleName: aws.String(userToResourceName(userName, "pod")),
			AssumeRolePolicyDocument: aws.String(`{
				"Version": "2012-10-17",
				"Statement": [
				  {
					"Sid": "",
					"Effect": "Allow",
					"Principal": {
					  "Service": "ecs-tasks.amazonaws.com"
					},
					"Action": "sts:AssumeRole"
				  }
				]
			  }
			  `),
		}

		createTaskRole, err := svc.CreateRole(createTaskRoleInput)
		if err != nil {
			return nil, fmt.Errorf("failed to create TaskRole: %s", err)
		}

		_, err = svc.AttachRolePolicy(&iam.AttachRolePolicyInput{
			PolicyArn: policy.Policy.Arn,
			RoleName:  aws.String(userToResourceName(userName, "pod")),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to attach RolePolicy: %s", err)
		}

		return createTaskRole.Role.Arn, nil
	}

}
