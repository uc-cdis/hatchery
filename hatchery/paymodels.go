package hatchery

import (
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/aws/aws-sdk-go/service/dynamodb/expression"
)

var NopaymodelsError = errors.New("No paymodels found")

func payModelsFromDatabase(userName string, current bool) (payModels *[]PayModel, err error) {
	// query pay model data for this user from DynamoDB
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region: aws.String("us-east-1"),
		},
	}))
	dynamodbSvc := dynamodb.New(sess)

	filt := expression.Name("user_id").Equal(expression.Value(userName))
	filt = filt.And(expression.Name("request_status").Equal(expression.Value("active")))
	if current {
		filt = filt.And(expression.Name("current_pay_model").Equal(expression.Value(true)))
	}
	expr, err := expression.NewBuilder().WithFilter(filt).Build()
	if err != nil {
		Config.Logger.Printf("Got error building expression: %s", err)
		return nil, err
	}

	params := &dynamodb.ScanInput{
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		FilterExpression:          expr.Filter(),
		TableName:                 aws.String(Config.Config.PayModelsDynamodbTable),
	}
	res, err := dynamodbSvc.Scan(params)
	if err != nil {
		Config.Logger.Printf("Query API call failed: %s", err)
		return nil, err
	}

	// Populate list of all available paymodels
	var payModelMap []PayModel
	err = dynamodbattribute.UnmarshalListOfMaps(res.Items, &payModelMap)
	if err != nil {
		return nil, err
	}

	return &payModelMap, nil
}

func payModelFromConfig(userName string) (pm *PayModel, err error) {
	var payModel PayModel
	for _, configPaymodel := range Config.PayModelMap {
		if configPaymodel.User == userName {
			payModel = configPaymodel
		}
	}
	if (PayModel{} == payModel) {
		return nil, NopaymodelsError
	}
	return &payModel, nil
}

func getCurrentPayModel(userName string) (result *PayModel, err error) {
	if Config.Config.PayModelsDynamodbTable == "" {
		// fallback for backward compatibility.
		// Multiple paymodels not supported
		pm, err := payModelFromConfig(userName)
		if err != nil {
			pm, err = getDefaultPayModel()
			if err != nil {
				return nil, NopaymodelsError
			}
		}
		return pm, nil
	}

	payModel := PayModel{}

	pm, err := payModelsFromDatabase(userName, true)

	if len(*pm) == 0 {
		pm, err := payModelFromConfig(userName)
		if err != nil {
			pm, err = getDefaultPayModel()
			if err != nil {
				return nil, nil
			}
		}
		return pm, nil
	}
	if len(*pm) == 1 {
		payModel = (*pm)[0]
		if err != nil {
			Config.Logger.Printf("Got error unmarshalling: %s", err)
			return nil, err
		}
	}
	if len(*pm) > 1 {
		// TODO: Reset to zero current paymodels here.
		// We don't want to be in a situation with multiple current paymodels
		return nil, fmt.Errorf("multiple current paymodels set")
	}
	return &payModel, nil
}

func getDefaultPayModel() (defaultPaymodel *PayModel, err error) {
	var pm PayModel
	if Config.Config.DefaultPayModel == pm {
		return nil, fmt.Errorf("no default paymodel set")
	}
	return &Config.Config.DefaultPayModel, nil
}

func getPayModelsForUser(userName string) (result *AllPayModels, err error) {
	if userName == "" {
		return nil, fmt.Errorf("no username sent in header")
	}
	PayModels := AllPayModels{}

	// Fallback to config-only if DynamoDB table is not configured
	if Config.Config.PayModelsDynamodbTable == "" {
		pm, err := payModelFromConfig(userName)
		if err != nil {
			pm, err = getDefaultPayModel()
			if err != nil {
				return nil, nil
			}
		}
		if pm == nil {
			return nil, NopaymodelsError
		}
		PayModels.CurrentPayModel = pm
		return &PayModels, nil
	}

	payModelMap, err := payModelsFromDatabase(userName, false)
	if err != nil {
		return nil, err
	}

	// temporary fallback to the config to get data for users that are not
	// in DynamoDB
	// TODO: remove this block once we only rely on DynamoDB
	payModel, err := payModelFromConfig(userName)
	if err == nil {
		*payModelMap = append(*payModelMap, *payModel)
	}

	PayModels.PayModels = *payModelMap

	payModel, err = getCurrentPayModel(userName)
	if err != nil {
		return nil, err
	}

	PayModels.CurrentPayModel = payModel

	return &PayModels, nil
}

func setCurrentPaymodel(userName string, workspaceid string) (paymodel *PayModel, err error) {
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region: aws.String("us-east-1"),
		},
	}))
	dynamodbSvc := dynamodb.New(sess)
	pm_db, err := payModelsFromDatabase(userName, false)
	if err != nil {
		return nil, err
	}
	pm_config, err := payModelFromConfig(userName)
	if err != nil {
		return nil, err
	}
	if pm_config.Id == workspaceid {
		resetCurrentPaymodel(userName, dynamodbSvc)
		return pm_config, nil
	}
	for _, pm := range *pm_db {
		if pm.Id == workspaceid {
			updateCurrentPaymodelInDB(userName, workspaceid, dynamodbSvc)
			return &pm, nil
		}
	}
	return nil, fmt.Errorf("No paymodel with id %s found for user %s", workspaceid, userName)
}

func updateCurrentPaymodelInDB(userName string, workspaceid string, svc *dynamodb.DynamoDB) error {
	// Reset current_pay_model for all paymodels first
	err := resetCurrentPaymodel(userName, svc)
	if err != nil {
		return err
	}
	// Set paymodel with id=workspaceid to current
	input := &dynamodb.UpdateItemInput{
		ExpressionAttributeNames: map[string]*string{
			"#CPM": aws.String("current_pay_model"),
		},
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":f": {
				BOOL: aws.Bool(true),
			},
		},
		Key: map[string]*dynamodb.AttributeValue{
			"user_id": {
				S: aws.String(userName),
			},
			"bmh_workspace_id": {
				S: aws.String(workspaceid),
			},
		},
		ReturnValues:     aws.String("ALL_NEW"),
		TableName:        aws.String(Config.Config.PayModelsDynamodbTable),
		UpdateExpression: aws.String("SET #CPM = :f"),
	}
	_, err = svc.UpdateItem(input)
	if err != nil {
		return err
	}
	return nil
}

func resetCurrentPaymodel(userName string, svc *dynamodb.DynamoDB) error {
	pm_db, err := payModelsFromDatabase(userName, false)
	if err != nil {
		return err
	}
	for _, pm := range *pm_db {
		input := &dynamodb.UpdateItemInput{
			ExpressionAttributeNames: map[string]*string{
				"#CPM": aws.String("current_pay_model"),
			},
			ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
				":f": {
					BOOL: aws.Bool(false),
				},
			},
			Key: map[string]*dynamodb.AttributeValue{
				"user_id": {
					S: aws.String(userName),
				},
				"bmh_workspace_id": {
					S: aws.String(pm.Id),
				},
			},
			ReturnValues:     aws.String("ALL_NEW"),
			TableName:        aws.String(Config.Config.PayModelsDynamodbTable),
			UpdateExpression: aws.String("SET #CPM = :f"),
		}
		_, err := svc.UpdateItem(input)
		if err != nil {
			return err
		}
	}
	return nil
}
