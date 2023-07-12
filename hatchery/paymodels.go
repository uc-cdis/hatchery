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

	filtActive := expression.Name("request_status").Equal(expression.Value("active"))
	filtAboveLimit := expression.Name("request_status").Equal(expression.Value("above limit"))
	filt := expression.Name("user_id").Equal(expression.Value(userName)).And(filtActive.Or(filtAboveLimit))

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
		Config.Logger.Printf("Got error unmarshalling paymodels: %s", err)
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

	var pm *[]PayModel

	if Config.Config.PayModelsDynamodbTable != "" {
		// Fetch pay models from DynamoDB with current_pay_model as `true`
		pm, err = payModelsFromDatabase(userName, true)
	}

	payModel := PayModel{}

	// If no dynamoDB or no current pay models in the DB,
	// fallback to defaultPayModel from config
	if pm == nil || len(*pm) == 0 {
		pm, err := getDefaultPayModel()
		if err != nil {
			return nil, nil
		}
		return pm, nil
	}

	// If more than one current pay model is found in the database
	if len(*pm) > 1 {
		// TODO: Reset to zero current pay models here.
		// We don't want to be in a situation with multiple current pay models
		return nil, fmt.Errorf("multiple current pay models set")
	}

	// If exactly one current pay model is found in the database
	payModel = (*pm)[0]
	if err != nil {
		Config.Logger.Printf("Got error unmarshalling: %s", err)
		return nil, err
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
	var payModelMap *[]PayModel

	if Config.Config.PayModelsDynamodbTable != "" {
		payModelMap, err = payModelsFromDatabase(userName, false)
		if err != nil {
			return nil, err
		}
	}
	currentPayModel, err := getCurrentPayModel(userName)
	if err != nil {
		return nil, err
	}

	// If `getCurrentPayModel` returns nil,
	// then there are no other paymodels to fallback to
	if currentPayModel == nil {
		return nil, nil
	}

	if payModelMap == nil {
		payModelMap = &[]PayModel{*currentPayModel}
	} else if len(*payModelMap) == 0 {
		*payModelMap = append(*payModelMap, *currentPayModel)
	} else if currentPayModel.Local {
		// If the user hasn't finalized a pay model, then instead of
		// automatically feeding the local pay model as current, we
		// append this to the rest of the pay models and empty the current pay model

		*payModelMap = append(*payModelMap, *currentPayModel)
		currentPayModel = nil
	}

	PayModels.PayModels = *payModelMap

	PayModels.CurrentPayModel = currentPayModel

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
		if err != NopaymodelsError {
			return nil, err
		}
	}
	if pm_config != nil {
		if pm_config.Id == workspaceid {
			err := resetCurrentPaymodel(userName, dynamodbSvc)
			if err != nil {
				return nil, err
			}
			return pm_config, nil
		}
	}
	for _, pm := range *pm_db {
		if pm.Id == workspaceid {
			err := updateCurrentPaymodelInDB(userName, workspaceid, dynamodbSvc)
			if err != nil {
				return nil, err
			}
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
