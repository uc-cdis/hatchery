package hatchery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
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

func GetDynamoDBSVC() *dynamodb.DynamoDB {
	var conf *aws.Config
	if os.Getenv("DYNAMODB_URL") != "" {
		Config.Logger.Printf("Region: %s\n", Config.Config.LicensesDynamodbRegion)
		Config.Logger.Printf("URL: %s\n", os.Getenv("DYNAMODB_URL"))
		conf = aws.NewConfig().WithEndpoint(os.Getenv("DYNAMODB_URL")).WithRegion(Config.Config.LicensesDynamodbRegion)
	} else {
		conf = aws.NewConfig().WithRegion(Config.Config.LicensesDynamodbRegion)
	}

	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: *conf,
	}))
	dynamodbSvc := dynamodb.New(sess)
	return dynamodbSvc
}
