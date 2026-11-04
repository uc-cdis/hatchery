package hatchery

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
)

// Create CloudWatch LogGroup for hatchery containers
func (sess *CREDS) CreateLogGroup(LogGroupName string, creds *aws.CredentialsCache) (string, error) {
	ctx := context.TODO()

	cfg := aws.Config{Region: "us-east-1", Credentials: creds}
	c := cloudwatchlogs.NewFromConfig(cfg)

	describeLogGroupIn := &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(LogGroupName),
	}

	logGroup, err := c.DescribeLogGroups(ctx, describeLogGroupIn)
	if err != nil {
		Config.Logger.Printf("Error in DescribeLogGroup: %s", err)
		return "", err
	}
	if len(logGroup.LogGroups) == 0 {
		Config.Logger.Printf("Creating LogGroup: %s", LogGroupName)
		createLogGroupIn := &cloudwatchlogs.CreateLogGroupInput{
			LogGroupName: aws.String(LogGroupName),
		}
		newLogGroup, err := c.CreateLogGroup(ctx, createLogGroupIn)
		if err != nil {
			Config.Logger.Printf("Error in  CreateLogGroup: %s, %s", err, newLogGroup)
			return "", err
		}
		return LogGroupName, nil
	} else {
		Config.Logger.Printf("LogGroup already exists: %s", LogGroupName)
	}
	return *logGroup.LogGroups[0].LogGroupName, nil
}
