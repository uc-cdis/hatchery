package hatchery

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/imagebuilder"
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

func TestGetNextflowInstanceAmi(t *testing.T) {
	defer SetupAndTeardownTest()()

	instanceAmiValue := "instance-ami"
	instanceAmiBuilderArnValue := "instance-ami-builder-arn"
	builderLatestAmi := "latest-ami"

	Config.ContainersMap = map[string]Container{
		"container_with_instance_ami": {
			NextflowConfig: NextflowConfig{
				InstanceAmi:           instanceAmiValue,
				InstanceAmiBuilderArn: "",
			},
		},
		"container_with_instance_ami_builder_arn": {
			NextflowConfig: NextflowConfig{
				InstanceAmi:           "",
				InstanceAmiBuilderArn: instanceAmiBuilderArnValue,
			},
		},
		"container_with_both": {
			NextflowConfig: NextflowConfig{
				InstanceAmi:           instanceAmiValue,
				InstanceAmiBuilderArn: instanceAmiBuilderArnValue,
			},
		},
		"container_with_neither": {
			NextflowConfig: NextflowConfig{
				InstanceAmi:           "",
				InstanceAmiBuilderArn: "",
			},
		},
	}

	// mock the `imagebuilder.ListImagePipelineImages` call to AWS
	mockedListImagePipelineImages := func(input *imagebuilder.ListImagePipelineImagesInput) (*imagebuilder.ListImagePipelineImagesOutput, error) {
		// on the 1st call, return an old image and a NextToken to trigger a 2nd call
		output := imagebuilder.ListImagePipelineImagesOutput{
			ImageSummaryList: []*imagebuilder.ImageSummary{
				{
					DateCreated: aws.String("2023-03-03T00:00:00Z"),
					OutputResources: &imagebuilder.OutputResources{
						Amis: []*imagebuilder.Ami{
							{
								Image: aws.String("old-ami"),
							},
						},
					},
				},
			},
			NextToken: aws.String("next-token"),
		}
		// on the 2nd call, return a new image and no NextToken
		if input.NextToken != nil {
			output = imagebuilder.ListImagePipelineImagesOutput{
				ImageSummaryList: []*imagebuilder.ImageSummary{
					{
						DateCreated: aws.String("2024-02-02T00:00:00Z"),
						OutputResources: &imagebuilder.OutputResources{
							Amis: []*imagebuilder.Ami{
								{
									Image: &builderLatestAmi,
								},
							},
						},
					},
				},
			}
		}
		return &output, nil
	}

	for containerId, container := range Config.ContainersMap {
		ami, err := getNextflowInstanceAmi(container.NextflowConfig, mockedListImagePipelineImages)
		if containerId == "container_with_neither" {
			if err == nil {
				t.Errorf("Expected `getNextflowInstanceAmi()` to error but it returned an AMI: '%s'", ami)
			}
			continue
		}
		if err != nil {
			t.Errorf("`getNextflowInstanceAmi()` failure: %v", err)
		}
		var expectedAmi string
		if containerId == "container_with_instance_ami" || containerId == "container_with_both" {
			expectedAmi = instanceAmiValue
		} else { // container_with_instance_ami_builder_arn
			expectedAmi = builderLatestAmi
		}
		if ami != expectedAmi {
			t.Errorf("Expected `getNextflowInstanceAmi()` to return '%s' but it returned '%s'", expectedAmi, ami)
		}
	}
}
