package hatchery

import (
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/aws/aws-sdk-go/service/dynamodb/expression"
	"github.com/google/uuid"
	//coreV1 "k8s.io/api/core/v1"
)

// TODO: move this to hatchery config
type Gen3UserLicense struct {
	ItemId      string `json:"itemId"`
	Environment string `json:"environment"`
	LicenseType string `json:"licenseType"`
	// try changing to bool
	IsActive           string `json:"isActive"`
	UserId             string `json:"userId"`
	LicenseId          int    `json:"licenseId"`
	FirstUsedTimestamp int    `json:"firstUsedTimestamp"`
	LastUsedTimestamp  int    `json:"lastUsedTimestamp"`
}

var ErrNoLicenseIds = errors.New("no license ids available")

func getActiveGen3UserLicenses() (gen3UserLicenses *[]Gen3UserLicense, err error) {
	// Query the table to get all active user license items

	// Move to config and get from environment variable
	targetEnvironment := "georget.planx-pla.net"
	// Maybe also put the global secondary index name in config
	Config.Logger.Printf("Ready to query table for active users: %s", Config.Config.Gen3UserLicenseTable)

	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region: aws.String("us-east-1"),
			// Use this endpoint for running locally
			// Endpoint: aws.String("http://localhost:8000"),
		},
	}))
	dynamodbSvc := dynamodb.New(sess)

	keyEx1 := expression.Key("environment").Equal(expression.Value(targetEnvironment))
	keyEx2 := expression.Key("isActive").Equal(expression.Value("True"))
	expr, err := expression.NewBuilder().WithKeyCondition(expression.KeyAnd(keyEx1, keyEx2)).Build()
	if err != nil {
		Config.Logger.Printf("Error in building expression for query: %s", err)
		return nil, err
	}
	res, err := dynamodbSvc.Query(&dynamodb.QueryInput{
		TableName:                 aws.String(Config.Config.Gen3UserLicenseTable),
		IndexName:                 aws.String("activeUsersIndex"),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		KeyConditionExpression:    expr.KeyCondition(),
	})
	if err != nil {
		Config.Logger.Printf("Error in active user query: %s", err)
		return nil, err
	}
	fmt.Println(res.Items)

	var userLicenses []Gen3UserLicense
	err = dynamodbattribute.UnmarshalListOfMaps(res.Items, &userLicenses)
	if err != nil {
		Config.Logger.Printf("Error in unmarshalling active users: %s", err)
		return nil, err
	}

	fmt.Println(userLicenses)
	return &userLicenses, nil
}

func getNextLicenseId(activeGen3UserLicenses *[]Gen3UserLicense, maxLicenseIds int) int {
	// Determine the next available licenseId [1..6], return 0 if no ids
	if len(*activeGen3UserLicenses) == 0 {
		return 1
	}
	var idInUsedIds bool
	for i := 1; i <= maxLicenseIds; i++ {
		idInUsedIds = false
		for _, v := range *activeGen3UserLicenses {
			if i == v.LicenseId {
				idInUsedIds = true
				break
			}
		}
		if !idInUsedIds {
			Config.Logger.Printf("Next available license id: %d", i)
			return i
		}
	}
	// No ids available
	return 0
}

func createGen3UserLicense(userId string, licenseId int) (gen3UserLicense Gen3UserLicense, err error) {
	// Create a new user-license object and put in table

	licenseType := "STATA-HEAL"
	// Move to config and get from environment variable
	targetEnvironment := "georget.planx-pla.net"
	// Maybe also put the global secondary index name in config
	Config.Logger.Printf("Ready to put item for new user license in table: %s", Config.Config.Gen3UserLicenseTable)

	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region: aws.String("us-east-1"),
			// Use this endpoint for running locally
			// Endpoint: aws.String("http://localhost:8000"),
		},
	}))
	dynamodbSvc := dynamodb.New(sess)

	itemId := uuid.New().String()
	currentUnixTime := int(time.Now().Unix())

	// create new Gen3UserLicense
	newItem := Gen3UserLicense{}
	newItem.LicenseType = licenseType
	newItem.ItemId = itemId
	newItem.Environment = targetEnvironment
	newItem.UserId = userId
	newItem.LicenseId = licenseId
	newItem.IsActive = "True"
	newItem.FirstUsedTimestamp = currentUnixTime
	newItem.LastUsedTimestamp = currentUnixTime

	// marshall Gen3UserLicense into dynamodb item
	item, err := dynamodbattribute.MarshalMap(newItem)

	// put item
	res, err := dynamodbSvc.PutItem(&dynamodb.PutItemInput{
		TableName: aws.String(Config.Config.Gen3UserLicenseTable),
		Item:      item,
	})
	if err != nil {
		Config.Logger.Printf("Error: could not add item to table: %s", err)
		return newItem, err
	}
	Config.Logger.Printf("Res: %s", res)
	// Return new gen3-user-license item
	return newItem, nil
}
