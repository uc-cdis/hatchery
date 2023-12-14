package hatchery

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/aws/aws-sdk-go/service/dynamodb/expression"
	"github.com/google/uuid"

	//coreV1 "k8s.io/api/core/v1"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	coreV1Types "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/clientcmd"
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

// TODO: fix the return values - should be 2
func getAllGen3UserLicenses() (gen3UserLicenses *[]Gen3UserLicense, err error) {
	// scan table to get all gen3 user licenses
	// move the table name to config
	// tableName := "gen3-user-license"
	Config.Logger.Printf("Ready to scan table: %s", Config.Config.Gen3UserLicenseTable)

	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region: aws.String("us-east-1"),
			// Use this endpoint for running locally
			// Endpoint: aws.String("http://localhost:8000"),
		},
	}))
	dynamodbSvc := dynamodb.New(sess)

	// TODO: Filter out active sessions and Stata license items
	params := &dynamodb.ScanInput{
		TableName: aws.String(Config.Config.Gen3UserLicenseTable),
	}
	res, err := dynamodbSvc.Scan(params)
	if err != nil {
		Config.Logger.Printf("Error in scan: %s", err)
		return nil, err
	}
	fmt.Println(res.Items)

	var userLicenses []Gen3UserLicense
	err = dynamodbattribute.UnmarshalListOfMaps(res.Items, &userLicenses)
	if err != nil {
		Config.Logger.Printf("Error in unmarshalling all gen3 user licenses: %s", err)
		return nil, err
	}

	fmt.Println(&userLicenses)

	return &userLicenses, nil
}

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
	//currentUnixTime := time.Now().Unix()

	// create new Gen3UserLicense
	newItem := Gen3UserLicense{}
	newItem.LicenseType = licenseType
	newItem.ItemId = itemId
	newItem.Environment = targetEnvironment
	newItem.UserId = userId
	newItem.LicenseId = licenseId
	newItem.IsActive = "True"
	//newItem.FirstUsedTimestamp = currentUnixTime

	// marshall Gen3UserLicens into dynamodb item
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
	// return response (the newly created item)
	return newItem, nil
}

func setGen3UserLicensInactive(itemId string) error {
	// Update an item to mark as inactive

	// Move to config and get from environment variable
	targetEnvironment := "georget.planx-pla.net"
	// Maybe also put the global secondary index name in config
	Config.Logger.Printf("Ready to update existing user license in table: %s", Config.Config.Gen3UserLicenseTable)
	isActive := "False"

	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region: aws.String("us-east-1"),
			// Use this endpoint for running locally
			// Endpoint: aws.String("http://localhost:8000"),
		},
	}))
	dynamodbSvc := dynamodb.New(sess)

	// pull out the input from UpdateItem - this matches paymodel and awsdocs
	input := &dynamodb.UpdateItemInput{
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":active": {
				S: aws.String(isActive),
			},
		},
		TableName: aws.String(Config.Config.Gen3UserLicenseTable),
		// Use the composite primary key: itemId, environment
		Key: map[string]*dynamodb.AttributeValue{
			"itemId": {
				S: aws.String(itemId),
			},
			"environment": {
				S: aws.String(targetEnvironment),
			},
		},
		ReturnValues:     aws.String("UPDATED_NEW"),
		UpdateExpression: aws.String("set isActive = :active"),
	}

	_, err := dynamodbSvc.UpdateItem(input)
	if err != nil {
		Config.Logger.Printf("Error: could not update item in table: %s\n", err)
		return err
	}
	return nil

}

func getGen3LiicenseString() (licenseString string, err error) {
	// Query the table to fetch the Gen3 Stata license string

	// Maybe also put the global secondary index name in config
	targetLicenseType := "STATA-HEAL"
	targetLicenseFlag := "licenseStringFlag"
	fmt.Println("Ready to query table for licenseString:", Config.Config.Gen3UserLicenseTable)

	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region: aws.String("us-east-1"),
			// Use this endpoint for running locally
			// Endpoint: aws.String("http://localhost:8000"),
		},
	}))
	dynamodbSvc := dynamodb.New(sess)

	// TODO filter on target environment
	keyEx1 := expression.Key("licenseType").Equal(expression.Value(targetLicenseType))
	keyEx2 := expression.Key("environment").Equal(expression.Value(targetLicenseFlag))
	expr, err := expression.NewBuilder().WithKeyCondition(expression.KeyAnd(keyEx1, keyEx2)).Build()
	if err != nil {
		Config.Logger.Printf("Error in building expression for query: %s", err)
		return "", err
	}
	res, err := dynamodbSvc.Query(&dynamodb.QueryInput{
		TableName:                 aws.String(Config.Config.Gen3UserLicenseTable),
		IndexName:                 aws.String("LicenseStringIndex"),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		KeyConditionExpression:    expr.KeyCondition(),
	})
	if err != nil {
		Config.Logger.Printf("Error in license string query: %s", err)
		return "", err
	}
	fmt.Println(res.Items)

	var userLicenses []Gen3UserLicense
	err = dynamodbattribute.UnmarshalListOfMaps(res.Items, &userLicenses)
	if err != nil {
		Config.Logger.Printf("Error in unmarshalling license string: %s", err)
		return "", err
	}
	licenseString = userLicenses[0].ItemId
	// Config.Logger.Printf("License string: %s", licenseString)
	return licenseString, nil
}

func getLicenseFromKubernetes() (licenseString string, err error) {
	// Read the stata-license string from the g3auto kubernetes secret
	g3autoName := "stata-workspace-gen3-license-g3auto"
	g3autoKey := "stata_license.txt"
	var secretsClient coreV1Types.SecretInterface

	namespace := "default"
	// QA has a .kube/config file but dev environments do not
	kubeconfig := os.Getenv("HOME") + "/.kube/config"
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	secretsClient = clientset.CoreV1().Secrets(namespace)
	secret, err := secretsClient.Get(context.TODO(), g3autoName, metaV1.GetOptions{})
	licenseString = fmt.Sprintf("%s", secret.Data[g3autoKey])

	return licenseString, nil

}
