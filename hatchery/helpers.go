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
	"github.com/aws/aws-sdk-go/aws/credentials" // TODO remove
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
	// roleARN := "arn:aws:iam::" + payModel.AWSAccountId + ":role/csoc_adminvm"
	// sess := awstrace.WrapSession(session.Must(session.NewSession(&aws.Config{
	// 	Region: aws.String(payModel.Region),
	// })))
	// creds := stscreds.NewCredentials(sess, roleARN)

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

	// create AWS batch job queue
	batchSvc := batch.New(session.Must(session.NewSession(&aws.Config{
		Region: aws.String("us-east-1"),
		// TODO update:
		Credentials: credentials.NewStaticCredentials(os.Getenv("AccessKeyId"), os.Getenv("SecretAccessKey"), ""),
	})))
	// batchSvc := batch.New(sess, &aws.Config{Credentials: creds})
	batchJobQueueName := fmt.Sprintf("nextflow-job-queue-%s", userName)
	input := &batch.CreateJobQueueInput{
		ComputeEnvironmentOrder: []*batch.ComputeEnvironmentOrder{
			{
				ComputeEnvironment: aws.String("arn:aws:batch:us-east-1:707767160287:compute-environment/nextflow-pauline-compute-env"), // TODO update
				Order: aws.Int64(int64(0)),
			},
		},
		JobQueueName: &batchJobQueueName,
		Priority: aws.Int64(int64(0)),
		Tags: tagsMap,
	}
	_, err := batchSvc.CreateJobQueue(input)
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
	IamSvc := iam.New(session.Must(session.NewSession(&aws.Config{
		Region: aws.String("us-east-1"),
		// TODO update:
		Credentials: credentials.NewStaticCredentials(os.Getenv("AccessKeyId"), os.Getenv("SecretAccessKey"), ""),
	})))
	policyName := fmt.Sprintf("nextflow-jobs-%s", userName)
	policyResult, err := IamSvc.CreatePolicy(&iam.CreatePolicyInput{
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
		PolicyName: &policyName,
		Path: aws.String(fmt.Sprintf("/%s/", tag)), // so we can use the path later to get the policy ARN
		Tags: tags,
	})
	policyArn := ""
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == iam.ErrCodeEntityAlreadyExistsException {
				Config.Logger.Printf("Debug: IAM policy '%s' already exists", policyName)
				listPoliciesResult, err := IamSvc.ListPolicies(&iam.ListPoliciesInput{
					PathPrefix: aws.String(fmt.Sprintf("/%s/", tag)),
				})
				if err != nil {
					Config.Logger.Printf("Error getting existing IAM policy '%s': %v", policyName, err)
					return "", "", err
				}
				policyArn = *listPoliciesResult.Policies[0].Arn
			} else {
				Config.Logger.Printf("Error creating IAM policy '%s': %v", policyName, aerr)
				return "", "", err
			}
		} else {
			Config.Logger.Printf("Error creating IAM policy '%s': %v", policyName, err)
			return "", "", err
		}
	} else {
		Config.Logger.Printf("Created IAM policy '%s'", policyName)
		policyArn = *policyResult.Policy.Arn
	}

	// create IAM policy and user for nextflow client

	nextflowUserName := fmt.Sprintf("nextflow-%s", userName)
	_, err = IamSvc.CreateUser(&iam.CreateUserInput{
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

	_, err = IamSvc.AttachUserPolicy(&iam.AttachUserPolicyInput{
		UserName: &nextflowUserName,
		PolicyArn: &policyArn, // TODO change this to the other policy
	})
	if err != nil {
		Config.Logger.Printf("Error attaching policy '%s' to user '%s': %v", policyName, nextflowUserName, err)
		return "", "", err
	} else {
		Config.Logger.Printf("Attached policy '%s' to user '%s'", policyName, nextflowUserName)
	}

	// create access key for the nextflow user
	accessKeyResult, err := IamSvc.CreateAccessKey(&iam.CreateAccessKeyInput{
		UserName: &nextflowUserName,
	})
	if err != nil {
		Config.Logger.Printf("Error creating access key for user '%s': %v", nextflowUserName, err)
		return "", "", err
	}
	keyId := *accessKeyResult.AccessKey.AccessKeyId
	keySecret := *accessKeyResult.AccessKey.SecretAccessKey
	Config.Logger.Printf("Created access key for user '%s': key ID '%v'", nextflowUserName, keyId)

	return keyId, keySecret, nil
}
