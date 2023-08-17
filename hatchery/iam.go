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
	pm, err := getCurrentPayModel(userName)
	if err != nil {
		return nil, err
	}
	policyArn := fmt.Sprintf("arn:aws:iam::%s:policy/%s", pm.AWSAccountId, fmt.Sprintf("ws-task-policy-%s", userName))
	taskRoleInput := &iam.GetRoleInput{
		RoleName: aws.String(userToResourceName(userName, "pod")),
	}
	taskRole, _ := svc.GetRole(taskRoleInput)
	if taskRole.Role != nil {
		return taskRole.Role.Arn, nil
	} else {
		policyAlreadyExists := false
		_, err := svc.CreatePolicy(&iam.CreatePolicyInput{
			PolicyDocument: aws.String(`{
				"Version": "2012-10-17",
				"Statement": [
					{
						"Effect": "Allow",
						"Action": [
							"elasticfilesystem:ClientMount",
							"elasticfilesystem:ClientWrite",
							"elasticfilesystem:ClientRootAccess"
						],
						"Resource": "*"
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
			PolicyArn: &policyArn,
			RoleName:  aws.String(userToResourceName(userName, "pod")),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to attach RolePolicy: %s", err)
		}

		return createTaskRole.Role.Arn, nil
	}

}

// https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task_execution_IAM_role.html
// The task execution role grants the Amazon ECS container and Fargate agents permission to make AWS API calls on your behalf.
const ecsTaskExecutionRoleName = "ecsTaskExecutionRole"
const ecsTaskExecutionPolicyArn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
const ecsTaskExecutionRoleAssumeRolePolicyDocument = `{
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
}`

func (creds *CREDS) CreateEcsTaskExecutionRole() (*string, error) {
	svc := iam.New(session.Must(session.NewSession(&aws.Config{
		Credentials: creds.creds,
		Region:      aws.String("us-east-1"),
	})))
	getRoleResp, err := svc.GetRole(
		&iam.GetRoleInput{
			RoleName: aws.String(ecsTaskExecutionRoleName),
		},
	)

	if err == nil {
		return getRoleResp.Role.Arn, nil
	}

	createRoleResp, err := svc.CreateRole(
		&iam.CreateRoleInput{
			AssumeRolePolicyDocument: aws.String(ecsTaskExecutionRoleAssumeRolePolicyDocument),
			RoleName:                 aws.String(ecsTaskExecutionRoleName),
		},
	)

	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	ecsTaskExecutionRoleArn := createRoleResp.Role.Arn

	_, err = svc.AttachRolePolicy(
		&iam.AttachRolePolicyInput{
			RoleName:  aws.String(ecsTaskExecutionRoleName),
			PolicyArn: aws.String(ecsTaskExecutionPolicyArn),
		},
	)

	if err != nil {
		fmt.Println(err)
		return nil, err
	}

	return ecsTaskExecutionRoleArn, nil
}

func createPolicyIfNotExist(iamSvc *iam.IAM, policyName string, pathPrefix *string, tags []*iam.Tag, policyDocument *string) (string, error) {
	policyResult, err := iamSvc.CreatePolicy(&iam.CreatePolicyInput{
		PolicyName:     &policyName,
		PolicyDocument: policyDocument,
		Path:           pathPrefix, // so we can use the path later to get the policy ARN
		Tags:           tags,
	})
	policyArn := ""
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == iam.ErrCodeEntityAlreadyExistsException {
				Config.Logger.Printf("Debug: policy '%s' already exists", policyName)
				listPoliciesResult, err := iamSvc.ListPolicies(&iam.ListPoliciesInput{
					PathPrefix: pathPrefix,
				})
				if err != nil {
					Config.Logger.Printf("Error getting existing policy '%s': %v", policyName, err)
					return "", err
				}
				policyArn = *listPoliciesResult.Policies[0].Arn
			} else {
				Config.Logger.Printf("Error creating policy '%s': %v", policyName, aerr)
				return "", err
			}
		} else {
			Config.Logger.Printf("Error creating policy '%s': %v", policyName, err)
			return "", err
		}
	} else {
		Config.Logger.Printf("Created policy '%s'", policyName)
		policyArn = *policyResult.Policy.Arn
	}
	return policyArn, nil
}
