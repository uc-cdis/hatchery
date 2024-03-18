package hatchery

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/aws/aws-sdk-go/service/dynamodb/expression"
	"github.com/google/uuid"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Gen3LicenseUserMap struct {
	ItemId             string `json:"itemId"`
	Environment        string `json:"environment"`
	LicenseType        string `json:"licenseType"`
	IsActive           string `json:"isActive"`
	UserId             string `json:"userId"`
	LicenseId          int    `json:"licenseId"`
	FirstUsedTimestamp int    `json:"firstUsedTimestamp"`
	LastUsedTimestamp  int    `json:"lastUsedTimestamp"`
}

var initializeDbConfig = func() *DbConfig {
	// Create a new dynamoDB client
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region: aws.String("us-east-1"),
			// Use this endpoint for running locally
			// Endpoint: aws.String("http://localhost:8000"),
		},
	}))
	return &DbConfig{
		DynamoDb: dynamodb.New(sess),
	}
}

var validateContainerLicenseInfo = func(containerName string, licenseInfo LicenseInfo) bool {

	ok := true
	if !licenseInfo.Enabled {
		fmt.Printf("Warning: License is not enabled for container %s\n", containerName)
		ok = false
	}
	if licenseInfo.LicenseType == "" {
		fmt.Printf("Error in container config. Empty LicenseType for container %s\n", containerName)
		ok = false
	}
	if licenseInfo.MaxLicenseIds == 0 {
		fmt.Printf("Error in container config. Empty or 0 MaxLicenseIds for container %s\n", containerName)
		ok = false
	}
	if licenseInfo.G3autoName == "" {
		fmt.Printf("Error in container config. Empty G3autoName for container %s\n", containerName)
		ok = false
	}
	if licenseInfo.G3autoKey == "" {
		fmt.Printf("Error in container config. Empty G3autoKey for container %s\n", containerName)
		ok = false
	}
	if licenseInfo.FilePath == "" {
		fmt.Printf("Error in container config. Empty FilePath for container %s\n", containerName)
		ok = false
	}
	if licenseInfo.WorkspaceFlavor == "" {
		fmt.Printf("Error in container config. Empty WorkspaceFlavor for container %s\n", containerName)
		ok = false
	}
	return ok
}

func getItemsFromQuery(dbconfig *DbConfig, queryInput *dynamodb.QueryInput) ([]map[string]*dynamodb.AttributeValue, error) {
	// Get items from a db query
	queryOutput, err := dbconfig.DynamoDb.Query(queryInput)
	if err != nil {
		return nil, err
	}
	tableItems := queryOutput.Items
	// If the query result is paginated then get the rest of the items
	for queryOutput.LastEvaluatedKey != nil {
		queryInput.ExclusiveStartKey = queryOutput.LastEvaluatedKey
		queryOutput, err = dbconfig.DynamoDb.Query(queryInput)
		if err != nil {
			return nil, err
		}
		tableItems = append(tableItems, queryOutput.Items...)
	}
	return tableItems, nil
}

var getActiveGen3LicenseUserMaps = func(dbconfig *DbConfig, container Container) (gen3LicenseUserMaps []Gen3LicenseUserMap, err error) {
	// Query the table to get all active gen3 license user map items
	emptyList := []Gen3LicenseUserMap{}

	targetEnvironment := os.Getenv("GEN3_ENDPOINT")
	ok := validateContainerLicenseInfo(container.Name, container.License)
	if !ok {
		Config.Logger.Printf("Gen3License not set up for container: %s", err)
		return emptyList, nil
	}

	// Query on global secondary index and filter by license type (eg. "STATA")
	keyEx1 := expression.Key("environment").Equal(expression.Value(aws.String(targetEnvironment)))
	keyEx2 := expression.Key("isActive").Equal(expression.Value("True"))
	filt := expression.Name("licenseType").Equal(expression.Value(container.License.LicenseType))
	expr, err := expression.NewBuilder().WithKeyCondition(expression.KeyAnd(keyEx1, keyEx2)).WithFilter(filt).Build()
	if err != nil {
		Config.Logger.Printf("Error in building expression for query: %s", err)
		return emptyList, err
	}
	queryUserMapsInput := &dynamodb.QueryInput{
		TableName:                 aws.String(Config.Config.LicenseUserMapsTable),
		IndexName:                 aws.String(Config.Config.LicenseUserMapsGSI),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		KeyConditionExpression:    expr.KeyCondition(),
		FilterExpression:          expr.Filter(),
	}
	licenseUserMapItems, err := getItemsFromQuery(dbconfig, queryUserMapsInput)
	if err != nil {
		Config.Logger.Printf("Error in active user query: %s", err)
		return emptyList, err
	}

	// Populate list of all active gen3 license user maps
	var gen3LicenseUsers []Gen3LicenseUserMap
	err = dynamodbattribute.UnmarshalListOfMaps(licenseUserMapItems, &gen3LicenseUsers)
	if err != nil {
		Config.Logger.Printf("Error in unmarshalling active gen3 license user maps: %s", err)
		return emptyList, err
	}
	Config.Logger.Printf("Debug: active gen3 license user maps %v", gen3LicenseUsers)
	return gen3LicenseUsers, nil
}

var getLicenseUserMapsForUser = func(dbconfig *DbConfig, userId string) (gen3LicenseUserMaps []Gen3LicenseUserMap, err error) {
	// Query the table to get all gen3 license user map items for user
	emptyList := []Gen3LicenseUserMap{}

	targetEnvironment := os.Getenv("GEN3_ENDPOINT")

	// Query on global secondary index and filter by userId
	keyEx1 := expression.Key("environment").Equal(expression.Value(aws.String(targetEnvironment)))
	keyEx2 := expression.Key("isActive").Equal(expression.Value("True"))
	filt := expression.Name("userId").Equal(expression.Value(userId))
	expr, err := expression.NewBuilder().WithKeyCondition(expression.KeyAnd(keyEx1, keyEx2)).WithFilter(filt).Build()
	if err != nil {
		Config.Logger.Printf("Error in building expression for query: %s", err)
		return emptyList, err
	}
	queryUserMapsInput := &dynamodb.QueryInput{
		TableName:                 aws.String(Config.Config.LicenseUserMapsTable),
		IndexName:                 aws.String(Config.Config.LicenseUserMapsGSI),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		KeyConditionExpression:    expr.KeyCondition(),
		FilterExpression:          expr.Filter(),
	}
	licenseUserMapItems, err := getItemsFromQuery(dbconfig, queryUserMapsInput)
	if err != nil {
		Config.Logger.Printf("Error in items for user query: %s", err)
		return emptyList, err
	}

	// Populate list of gen3 license user maps for user
	var gen3LicenseUsers []Gen3LicenseUserMap
	err = dynamodbattribute.UnmarshalListOfMaps(licenseUserMapItems, &gen3LicenseUsers)
	if err != nil {
		Config.Logger.Printf("Error in unmarshalling gen3 license user maps for user: %s", err)
		return emptyList, err
	}
	Config.Logger.Printf("Debug: gen3 license user maps for user %v", gen3LicenseUsers)
	return gen3LicenseUsers, nil
}

func getNextLicenseId(activeGen3LicenseUserMaps []Gen3LicenseUserMap, maxLicenseIds int) int {
	// Determine the next available licenseId [1..6], return 0 if no ids
	if len(activeGen3LicenseUserMaps) == 0 {
		return 1
	}
	var idInUsedIds bool
	for i := 1; i <= maxLicenseIds; i++ {
		idInUsedIds = false
		for _, v := range activeGen3LicenseUserMaps {
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

var createGen3LicenseUserMap = func(dbconfig *DbConfig, userId string, licenseId int, container Container) (gen3LicenseUserMap Gen3LicenseUserMap, err error) {
	// Create a new user-license object and put in table

	targetEnvironment := os.Getenv("GEN3_ENDPOINT")
	// Maybe also put the global secondary index name in config

	itemId := uuid.New().String()
	currentUnixTime := int(time.Now().Unix())

	// create new Gen3LicenseUserMap
	newItem := Gen3LicenseUserMap{}
	newItem.LicenseType = container.License.LicenseType
	newItem.ItemId = itemId
	newItem.Environment = targetEnvironment
	newItem.UserId = userId
	newItem.LicenseId = licenseId
	newItem.IsActive = "True"
	newItem.FirstUsedTimestamp = currentUnixTime
	newItem.LastUsedTimestamp = currentUnixTime

	// marshall Gen3LicenseUserMap into dynamodb item
	item, err := dynamodbattribute.MarshalMap(newItem)
	if err != nil {
		Config.Logger.Printf("Error: could not marshal new item: %s", err)
		return newItem, err
	}
	// put item
	_, err = dbconfig.DynamoDb.PutItem(&dynamodb.PutItemInput{
		TableName: aws.String(Config.Config.LicenseUserMapsTable),
		Item:      item,
	})
	if err != nil {
		Config.Logger.Printf("Error: could not add item to table: %s", err)
		return newItem, err
	}
	// Return the new gen3-user-license item that we created; DynamoDB:putItem does not return new items.
	return newItem, nil
}

var setGen3LicenseUserInactive = func(dbconfig *DbConfig, itemId string) (Gen3LicenseUserMap, error) {
	// Update an item to mark as inactive

	targetEnvironment := os.Getenv("GEN3_ENDPOINT")
	// Maybe also put the global secondary index name in config

	isActive := "False"
	currentUnixTime := int(time.Now().Unix())
	// Mark the lastUsedTimestamp
	input := &dynamodb.UpdateItemInput{
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":active": {
				S: aws.String(isActive),
			},
			":currentTime": {
				N: aws.String(strconv.Itoa(currentUnixTime)),
			},
		},
		TableName: aws.String(Config.Config.LicenseUserMapsTable),
		// Use the composite primary key: itemId, environment
		Key: map[string]*dynamodb.AttributeValue{
			"itemId": {
				S: aws.String(itemId),
			},
			"environment": {
				S: aws.String(targetEnvironment),
			},
		},
		// AWS docs are bad: 'UPDATED_NEW' is not an accepted value.
		// Allowable values are 'NONE' or 'ALL_OLD' and no new values are returned.
		ReturnValues:     aws.String("UPDATED_NEW"),
		UpdateExpression: aws.String("set isActive = :active, lastUsedTimestamp = :currentTime"),
	}

	res, err := dbconfig.DynamoDb.UpdateItem(input)
	if err != nil {
		Config.Logger.Printf("Error: could not update item in table: %s", err)
		return Gen3LicenseUserMap{}, err
	}

	var updatedItem Gen3LicenseUserMap
	err = dynamodbattribute.UnmarshalMap(res.Attributes, &updatedItem)
	if err != nil {
		Config.Logger.Printf("Error: could not unmarshal updated item: %s", err)
		return Gen3LicenseUserMap{}, err
	}

	return updatedItem, nil

}

// Get the file-path related configurations
func getLicenceFilePathConfigs() []LicenseInfo {
	var config LicenseInfo
	var filePathConfigs []LicenseInfo

	for _, v := range Config.ContainersMap {
		if v.License.Enabled {
			validateContainerLicenseInfo(v.Name, v.License)
			config.FilePath = v.License.FilePath
			config.WorkspaceFlavor = v.License.WorkspaceFlavor
			config.G3autoName = v.License.G3autoName
			config.G3autoKey = v.License.G3autoKey
			filePathConfigs = append(filePathConfigs, config)
		}
	}
	return filePathConfigs
}

func filePathInLicenseConfigs(filePath string, configs []LicenseInfo) bool {
	for _, v := range configs {
		if filePath == v.FilePath {
			return true
		}
	}
	return false
}

func getG3autoInfoForFilepath(filePath string, configs []LicenseInfo) (string, string, bool) {
	for _, v := range configs {
		if filePath == v.FilePath {
			return v.G3autoName, v.G3autoKey, true
		}
	}
	return "", "", false
}

var getKubeClientSet = func() (clientset kubernetes.Interface, err error) {
	// Get the kubernetes client set
	kubeConfigPath := os.Getenv("HOME") + "/.kube/config"
	if _, err := os.Stat(kubeConfigPath); err == nil {
		// out of cluster, eg local
		config, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
		if err != nil {
			Config.Logger.Printf("Error: Could not build config for out of cluster client, %s", err)
			return nil, err
		}
		clientset, err = kubernetes.NewForConfig(config)
		if err != nil {
			Config.Logger.Printf("Error: Could not create clientset for out of cluster client, %s", err)
			return nil, err
		}
	} else {
		// in cluster
		config, err := rest.InClusterConfig()
		if err != nil {
			Config.Logger.Printf("Error: Could not build config for in cluster client, %s", err)
			return nil, err
		}
		clientset, err = kubernetes.NewForConfig(config)
		if err != nil {
			Config.Logger.Printf("Error: Could not create clientset for in cluster client, %s", err)
			return nil, err
		}
	}

	return clientset, nil

}

var getLicenseFromKubernetes = func(clientset kubernetes.Interface, g3autoName string, g3autoKey string) (licenseString string, err error) {
	// Read the gen3-license string from the g3auto kubernetes secret
	var namespace string
	var ok bool

	if namespace, ok = Config.Config.Sidecar.Env["NAMESPACE"]; ok {
		Config.Logger.Printf("Searching configured namespace for g3auto secret: %s", namespace)
	} else {
		Config.Logger.Printf("Error: namespace is not configured. Will try 'default'")
		namespace = "default"
	}

	//var secretsClient coreV1Types.SecretInterface
	secretsClient := clientset.CoreV1().Secrets(namespace)
	secret, err := secretsClient.Get(context.TODO(), g3autoName, metaV1.GetOptions{})
	if err != nil {
		Config.Logger.Printf("Error: could not get secret from kubernetes: %s", err)
		return "", err
	}
	licenseString = string(secret.Data[g3autoKey])
	// some g3auto secrets may have multiple strings separated by newlines
	licenseString = strings.Split(licenseString, "\n")[0]

	return licenseString, nil

}
