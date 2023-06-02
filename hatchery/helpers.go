package hatchery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	// "github.com/aws/aws-sdk-go/aws/credentials" // TODO remove
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/batch"
	"github.com/aws/aws-sdk-go/service/iam"
)

type APIKeyStruct struct {
	APIKey string `json:"api_key"`
	KeyID  string `json:"key_id"`
}

type WorkspaceKernelStatusStruct struct {
	LastActivityTime string `json:"last_activity"`
}

func StrToInt(str string) (string, error) {
	nonFractionalPart := strings.Split(str, ".")
	return nonFractionalPart[0], nil
}

func mem(str string) (string, error) {
	res := regexp.MustCompile(`(\d*)([M|G])ib?`)
	matches := res.FindStringSubmatch(str)
	num, err := strconv.Atoi(matches[1])
	if err != nil {
		return "", err
	}
	if matches[2] == "G" {
		num = num * 1024
	}
	return strconv.Itoa(num), nil
}

func cpu(str string) (string, error) {
	num, err := strconv.Atoi(str[:strings.IndexByte(str, '.')])
	if err != nil {
		return "", err
	}
	num = num * 1024
	return strconv.Itoa(num), nil
}

// Escapism escapes characters not allowed into hex with -
func escapism(input string) string {
	safeBytes := "abcdefghijklmnopqrstuvwxyz0123456789"
	var escaped string
	for _, v := range input {
		if !characterInString(v, safeBytes) {
			hexCode := fmt.Sprintf("%2x", v)
			escaped += "-" + hexCode
		} else {
			escaped += string(v)
		}
	}
	return escaped
}

func characterInString(a rune, list string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}

func truncateString(str string, num int) string {
	bnoden := str
	if len(str) > num {
		bnoden = str[0:num]
	}
	if bnoden[len(bnoden)-1] == '-' {
		bnoden = bnoden[0 : len(bnoden)-2]
	}
	return bnoden
}

// API key related helper functions
// Make http request with header and body
func MakeARequestWithContext(ctx context.Context, method string, apiEndpoint string, accessToken string, contentType string, headers map[string]string, body *bytes.Buffer) (*http.Response, error) {
	if headers == nil {
		headers = make(map[string]string)
	}
	if accessToken != "" {
		headers["Authorization"] = "Bearer " + accessToken
	}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	client := &http.Client{Timeout: 10 * time.Second}
	var req *http.Request
	var err error
	if body == nil {
		req, err = http.NewRequestWithContext(ctx, method, apiEndpoint, nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, apiEndpoint, body)
	}

	if err != nil {
		return nil, errors.New("Error occurred during generating HTTP request: " + err.Error())
	}
	for k, v := range headers {
		req.Header.Add(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Error occurred during making HTTP request: " + err.Error())
	}
	return resp, nil
}

func getFenceURL() string {
	fenceURL := "http://fence-service/"
	_, ok := os.LookupEnv("GEN3_ENDPOINT")
	if ok {
		fenceURL = "https://" + os.Getenv("GEN3_ENDPOINT") + "/user/"
	}
	return fenceURL
}

func getAmbassadorURL() string {
	ambassadorURL := "http://ambassador-service/"
	_, ok := os.LookupEnv("GEN3_ENDPOINT")
	if ok {
		ambassadorURL = "https://" + os.Getenv("GEN3_ENDPOINT") + "/lw-workspace/proxy/"
	}
	return ambassadorURL
}

func getAPIKeyWithContext(ctx context.Context, accessToken string) (apiKey *APIKeyStruct, err error) {
	if accessToken == "" {
		return nil, errors.New("No valid access token")
	}

	fenceAPIKeyURL := getFenceURL() + "credentials/api/"
	body := bytes.NewBufferString("{\"scope\": [\"data\", \"user\"]}")

	resp, err := MakeARequestWithContext(ctx, "POST", fenceAPIKeyURL, accessToken, "application/json", nil, body)
	if err != nil {
		return nil, err
	}

	if resp != nil && resp.StatusCode != 200 {
		b, _ := ioutil.ReadAll(resp.Body)
		Config.Logger.Print(string(b))
		return nil, errors.New("Error occurred when creating API key with error code " + strconv.Itoa(resp.StatusCode))
	}
	defer resp.Body.Close()

	fenceApiKeyResponse := new(APIKeyStruct)
	err = json.NewDecoder(resp.Body).Decode(fenceApiKeyResponse)
	if err != nil {
		return nil, errors.New("Unable to decode API key response: " + err.Error())
	}
	return fenceApiKeyResponse, nil
}

func deleteAPIKeyWithContext(ctx context.Context, accessToken string, apiKeyID string) error {
	if accessToken == "" {
		return errors.New("No valid access token")
	}

	fenceDeleteAPIKeyURL := getFenceURL() + "credentials/api/" + apiKeyID
	resp, err := MakeARequestWithContext(ctx, "DELETE", fenceDeleteAPIKeyURL, accessToken, "", nil, nil)
	if err != nil {
		return err
	}
	if resp != nil && resp.StatusCode != 204 {
		return errors.New("Error occurred when deleting API key with error code " + strconv.Itoa(resp.StatusCode))
	}
	return nil
}

func getKernelIdleTimeWithContext(ctx context.Context, accessToken string) (lastActivityTime int64, err error) {
	if accessToken == "" {
		return -1, errors.New("No valid access token")
	}

	workspaceKernelStatusURL := getAmbassadorURL() + "api/status"
	resp, err := MakeARequestWithContext(ctx, "GET", workspaceKernelStatusURL, accessToken, "", nil, nil)
	if err != nil {
		return -1, err
	}
	if resp != nil && resp.StatusCode != 200 {
		return -1, errors.New("Error occurred when getting workspace kernel status with error code " + strconv.Itoa(resp.StatusCode))
	}
	defer resp.Body.Close()

	workspaceKernelStatusResponse := new(WorkspaceKernelStatusStruct)
	err = json.NewDecoder(resp.Body).Decode(workspaceKernelStatusResponse)
	if err != nil {
		return -1, errors.New("Unable to decode workspace kernel status response: " + err.Error())
	}
	lastAct, err := time.Parse(time.RFC3339, workspaceKernelStatusResponse.LastActivityTime)
	if err != nil {
		return -1, errors.New("Unable to parse last activity time: " + err.Error())
	}
	return lastAct.Unix() * 1000, nil
}

func createNextflowResources(userName string) (string, string, error) { // TODO move to a different file
	// TODO are the resources shared between all QA envs / between staging and prod if they are in
	// the same account? Add the env to all the names?

	// roleARN := "arn:aws:iam::" + payModel.AWSAccountId + ":role/csoc_adminvm"
	// sess := awstrace.WrapSession(session.Must(session.NewSession(&aws.Config{
	// 	Region: aws.String(payModel.Region),
	// })))
	// creds := stscreds.NewCredentials(sess, roleARN)
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region: aws.String("us-east-1"),
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

	// create IAM policy and role for nextflow-created jobs
	policyName := fmt.Sprintf("nextflow-jobs-%s", userName)
	policyResult, err := iamSvc.CreatePolicy(&iam.CreatePolicyInput{
		PolicyName: &policyName,
		PolicyDocument: aws.String(fmt.Sprintf(`{
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
		}`, userName)),
		Path: pathPrefix, // so we can use the path later to get the policy ARN
		Tags: tags,
	})
	nextflowJobsPolicyArn := ""
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == iam.ErrCodeEntityAlreadyExistsException {
				Config.Logger.Printf("Debug: policy '%s' already exists", policyName)
				listPoliciesResult, err := iamSvc.ListPolicies(&iam.ListPoliciesInput{
					PathPrefix: pathPrefix,
				})
				if err != nil {
					Config.Logger.Printf("Error getting existing policy '%s': %v", policyName, err)
					return "", "", err
				}
				nextflowJobsPolicyArn = *listPoliciesResult.Policies[0].Arn
			} else {
				Config.Logger.Printf("Error creating policy '%s': %v", policyName, aerr)
				return "", "", err
			}
		} else {
			Config.Logger.Printf("Error creating policy '%s': %v", policyName, err)
			return "", "", err
		}
	} else {
		Config.Logger.Printf("Created policy '%s'", policyName)
		nextflowJobsPolicyArn = *policyResult.Policy.Arn
	}

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

	_, err = iamSvc.AttachRolePolicy(&iam.AttachRolePolicyInput{
		PolicyArn: &nextflowJobsPolicyArn,
		RoleName: &roleName,
	})
	if err != nil {
		Config.Logger.Printf("Error attaching role '%s' to policy '%s': %v", roleName, policyName, err)
		return "", "", err
	} else {
		Config.Logger.Printf("Attached role '%s' to policy '%s'", roleName, policyName)
	}

	// create IAM policy and user for nextflow client
	// TODO function to create policy if not exists and return policyArn
	policyName = fmt.Sprintf("nextflow-%s", userName)
	policyResult, err = iamSvc.CreatePolicy(&iam.CreatePolicyInput{
		PolicyName: &policyName,
		// TODO check job-definition/*
		PolicyDocument: aws.String(fmt.Sprintf(`{
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
		}`, batchJobQueueName, userName, nextflowJobsRoleArn)),
		Path: pathPrefix, // so we can use the path later to get the policy ARN
		Tags: tags,
	})
	nextflowPolicyArn := ""
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == iam.ErrCodeEntityAlreadyExistsException {
				Config.Logger.Printf("Debug: policy '%s' already exists", policyName)
				listPoliciesResult, err := iamSvc.ListPolicies(&iam.ListPoliciesInput{
					PathPrefix: pathPrefix,
				})
				if err != nil {
					Config.Logger.Printf("Error getting existing policy '%s': %v", policyName, err)
					return "", "", err
				}
				nextflowPolicyArn = *listPoliciesResult.Policies[0].Arn
			} else {
				Config.Logger.Printf("Error creating policy '%s': %v", policyName, aerr)
				return "", "", err
			}
		} else {
			Config.Logger.Printf("Error creating policy '%s': %v", policyName, err)
			return "", "", err
		}
	} else {
		Config.Logger.Printf("Created policy '%s'", policyName)
		nextflowPolicyArn = *policyResult.Policy.Arn
	}

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
