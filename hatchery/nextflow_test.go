package hatchery

import (
	"encoding/base64"
	"fmt"
	"testing"
)

func TestReplaceAllUsernamePlaceholders(t *testing.T) {
	defer SetupAndTeardownTest()()

	initialArray := []string{"quay.io/cdis/*:*", "1234.ecr.aws/nextflow-repo/{{username}}"}
	userName := "test-escaped-username"
	replacedArray := replaceAllUsernamePlaceholders(initialArray, userName)
	expectedOutput := []string{"quay.io/cdis/*:*", fmt.Sprintf("1234.ecr.aws/nextflow-repo/%s", userName)}

	errMsg := fmt.Sprintf("The 'replaceUsernamePlaceholder' function should have returned the expected output '%v', but it returned: '%v'", expectedOutput, replacedArray)
	if len(replacedArray) != len(expectedOutput) {
		t.Error(errMsg)
	}
	for i := range replacedArray {
		if replacedArray[i] != expectedOutput[i] {
			t.Error(errMsg)
		}
	}
}

func TestGenerateEcrLoginUserData(t *testing.T) {
	defer SetupAndTeardownTest()()

	jobImageWhitelist := []string{"1234.ecr.aws/repo1:tagA", "1234.ecr.aws/repo/without/tag", "quay.io/cdis/*:*", "1234.ecr.aws/nextflow-repo/{{username}}:tagB"}
	userName := "test-escaped-username"
	userData := generateEcrLoginUserData(jobImageWhitelist, userName)
	expectedOutput := `MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="==MYBOUNDARY=="

--==MYBOUNDARY==
Content-Type: text/cloud-config; charset="us-ascii"

packages:
- aws-cli
runcmd:
- aws ecr get-login-password --region us-east-1 | docker login --username AWS --password-stdin 1234.ecr.aws/repo1
- aws ecr get-login-password --region us-east-1 | docker login --username AWS --password-stdin 1234.ecr.aws/repo/without/tag
- aws ecr get-login-password --region us-east-1 | docker login --username AWS --password-stdin 1234.ecr.aws/nextflow-repo/test-escaped-username
--==MYBOUNDARY==--`

	if userData != base64.StdEncoding.EncodeToString([]byte(expectedOutput)) {
		t.Errorf("The 'generateEcrLoginUserData' function should have returned the expected output '%v', but it returned: '%v'", expectedOutput, userData)
	}
}
