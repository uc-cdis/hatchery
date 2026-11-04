package hatchery

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

var ErrNopaymodels = errors.New("no paymodels found")

var getPayModelTableCreds = func(cfg aws.Config) aws.Config {

	// Assume a new role if we have a pay model arn
	if Config.Config.PayModelsDynamodbArn != "" {
		stsClient := sts.NewFromConfig(cfg)
		provider := stscreds.NewAssumeRoleProvider(stsClient, Config.Config.PayModelsDynamodbArn)
		cfg.Credentials = aws.NewCredentialsCache(provider)
	}
	// In v1, the region and default creds came from the session. In v2, session no longer exists. cfg was already loaded config.WithRegion("us-east-1")) at line 38, so returning it unchanged produces the same outcome — default credentials, us-east-1 region.
	return cfg
}

var payModelsFromDatabase = func(userName string, current bool) (payModels *[]PayModel, err error) {
	ctx := context.TODO()
	// query pay model data for this user from DynamoDB
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		return nil, err
	}
	payModelTableConfig := getPayModelTableCreds(cfg)
	dynamodbSvc := dynamodb.NewFromConfig(payModelTableConfig)

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
	res, err := dynamodbSvc.Scan(ctx, params)
	if err != nil {
		Config.Logger.Printf("Query API call failed: %s", err)
		return nil, err
	}

	// Populate list of all available paymodels
	var payModelMap []PayModel
	err = attributevalue.UnmarshalListOfMapsWithOptions(res.Items, &payModelMap, func(o *attributevalue.DecoderOptions) {
		o.TagKey = "json"
	})
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
		return nil, ErrNopaymodels
	}
	return &payModel, nil
}

var getCurrentPayModel = func(userName string) (result *PayModel, err error) {

	var pm *[]PayModel

	if Config != nil && Config.Config.PayModelsDynamodbTable == "" {
		pm, err := getDefaultPayModel()
		if err != nil {
			return nil, nil
		}
		return pm, nil
	}

	// Fetch pay models from DynamoDB with current_pay_model as `true`
	pm, err = payModelsFromDatabase(userName, true)

	// If no current pay models in the DB,
	// see if there are any active paymodels for the user
	if pm == nil || len(*pm) == 0 {
		activePayModels, _ := payModelsFromDatabase(userName, false)

		if activePayModels != nil && len(*activePayModels) > 0 {
			// Terminating a workspace resets current_pay_model to false on every
			// row, so a user with exactly one pay model is left with nothing
			// current and cannot launch until they re-select it. Where that
			// choice is not surfaced to users, treat the only option as current.
			if Config.Config.AutoSelectSinglePayModel && len(*activePayModels) == 1 {
				return &(*activePayModels)[0], nil
			}
			// return nil since there is no current paymodel set by the user
			return nil, nil
		}

		// Fallback to default paymodel since
		// there is no persistent pay model for the user yet
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
	payModel := (*pm)[0]
	if err != nil {
		Config.Logger.Printf("Got error unmarshalling: %s", err)
		return nil, err
	}
	return &payModel, nil
}

var getDefaultPayModel = func() (defaultPaymodel *PayModel, err error) {
	var pm PayModel
	if Config.Config.DefaultPayModel == pm {
		return nil, fmt.Errorf("no default paymodel set")
	}
	return &Config.Config.DefaultPayModel, nil
}

var getPayModelsForUser = func(userName string) (result *AllPayModels, err error) {
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

	// If payModelMap is empty and `getCurrentPayModel` returns a paymodel,
	// Update payModelMap with it
	if currentPayModel != nil {
		if payModelMap == nil {
			payModelMap = &[]PayModel{*currentPayModel}
		} else if len(*payModelMap) == 0 {
			*payModelMap = append(*payModelMap, *currentPayModel)
		}
	}
	if currentPayModel == nil && payModelMap == nil {
		return nil, nil
	}

	PayModels.PayModels = *payModelMap

	PayModels.CurrentPayModel = currentPayModel

	return &PayModels, nil
}

var setCurrentPaymodel = func(userName string, workspaceid string) (paymodel *PayModel, err error) {
	ctx := context.TODO()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		return nil, err
	}
	payModelTableConfig := getPayModelTableCreds(cfg)
	dynamodbSvc := dynamodb.NewFromConfig(payModelTableConfig)
	pm_db, err := payModelsFromDatabase(userName, false)
	if err != nil {
		return nil, err
	}
	pm_config, err := payModelFromConfig(userName)
	if err != nil {
		if err != ErrNopaymodels {
			return nil, err
		}
	}
	if pm_config != nil {
		if pm_config.Id == workspaceid {
			err := resetCurrentPaymodelInDB(userName, dynamodbSvc)
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
	return nil, fmt.Errorf("no paymodel with id %s found for user %s", workspaceid, userName)
}

var resetCurrentPaymodel = func(userName string) error {
	ctx := context.TODO()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		return err
	}
	payModelTableConfig := getPayModelTableCreds(cfg)
	dynamodbSvc := dynamodb.NewFromConfig(payModelTableConfig)

	return resetCurrentPaymodelInDB(userName, dynamodbSvc)
}

func updateCurrentPaymodelInDB(userName string, workspaceid string, svc DynamoDBAPI) error {
	ctx := context.TODO()
	// Reset current_pay_model for all paymodels first
	err := resetCurrentPaymodelInDB(userName, svc)
	if err != nil {
		return err
	}
	// Set paymodel with id=workspaceid to current
	input := &dynamodb.UpdateItemInput{
		ExpressionAttributeNames: map[string]string{
			"#CPM": "current_pay_model",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":f": &types.AttributeValueMemberBOOL{Value: true},
		},
		Key: map[string]types.AttributeValue{
			"user_id":          &types.AttributeValueMemberS{Value: userName},
			"bmh_workspace_id": &types.AttributeValueMemberS{Value: workspaceid},
		},
		ReturnValues:     types.ReturnValueAllNew,
		TableName:        aws.String(Config.Config.PayModelsDynamodbTable),
		UpdateExpression: aws.String("SET #CPM = :f"),
	}
	_, err = svc.UpdateItem(ctx, input)
	if err != nil {
		return err
	}
	return nil
}

func resetCurrentPaymodelInDB(userName string, svc DynamoDBAPI) error {
	ctx := context.TODO()
	pm_db, err := payModelsFromDatabase(userName, false)
	if err != nil {
		return err
	}
	for _, pm := range *pm_db {
		input := &dynamodb.UpdateItemInput{
			ExpressionAttributeNames: map[string]string{
				"#CPM": "current_pay_model",
			},
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":f": &types.AttributeValueMemberBOOL{Value: false},
			},
			Key: map[string]types.AttributeValue{
				"user_id":          &types.AttributeValueMemberS{Value: userName},
				"bmh_workspace_id": &types.AttributeValueMemberS{Value: pm.Id},
			},
			ReturnValues:     types.ReturnValueAllNew,
			TableName:        aws.String(Config.Config.PayModelsDynamodbTable),
			UpdateExpression: aws.String("SET #CPM = :f"),
		}
		_, err := svc.UpdateItem(ctx, input)
		if err != nil {
			return err
		}
	}
	return nil
}
