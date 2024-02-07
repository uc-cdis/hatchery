package hatchery

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudwatchlogs"
)

// Create CloudWatch LogGroup for hatchery containers
func (sess *CREDS) CreateLogGroup(LogGroupName string, creds *credentials.Credentials) (string, error) {
	c := cloudwatchlogs.New(session.Must(session.NewSession(&aws.Config{
		Credentials: creds,
		Region:      aws.String("us-east-1"),
	})))

	describeLogGroupIn := &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(LogGroupName),
	}

	logGroup, err := c.DescribeLogGroups(describeLogGroupIn)
	if err != nil {
		Config.Logger.Printf("Error in DescribeLogGroup: %s", err)
		return "", err
	}
	if len(logGroup.LogGroups) == 0 {
		Config.Logger.Printf("Creating LogGroup: %s", LogGroupName)
		createLogGroupIn := &cloudwatchlogs.CreateLogGroupInput{
			LogGroupName: aws.String(LogGroupName),
		}
		newLogGroup, err := c.CreateLogGroup(createLogGroupIn)
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
