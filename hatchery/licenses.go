package hatchery

import (

	// AWS

	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/aws/aws-sdk-go/service/dynamodb/expression"
)

// type License struct {
// 	Name        string          `json:"name"`
// 	UserLimit   int             `json:"userLimit"`
// 	LicenseData string          `json:"licenseData"`
// 	Users       map[string]User `json:"users"`
// 	// not marshalled
// 	fileName string            `json:"-"`
// 	updates  chan *Transaction `json:"-"`
// 	logger   *log.Logger
// }

type License struct {
	LicenseName  string
	UserLimit    int
	LicenseData  string
	LicenseUsers map[string]int64
}

const (
	TIMEOUT_SECONDS       = 60
	MAX_CHECKOUT_ATTEMPTS = 5
	LICENSE_TABLE         = "licenses-test"
)

// TODO
// Create a table for the licenses
// add Stata, determine schema
// query for stata via CLI
// add a function for querying licenses
// checkout license via CLI
// convert to function
// function to renew license
// function to release license
// tests (?)

// aws dynamodb create-table --attribute-definitions AttributeName=name,AttributeType=S --table-name licenses-test --key-schema AttributeName=name,KeyType=HASH --provisioned-throughput ReadCapacityUnits=1,WriteCapacityUnits=1
// aws dynamodb scan --table-name licenses-test
// aws dynamodb put-item --table-name licenses-test  --item '{"name": {"S":"test1"}}'

// License
//	{
//		"name": "STATA-HEAL",
//		"licenseData":	"asfdwer",
//		"userLimit": 6,
//		"users": {
//			[username]: 166902348, //timestamp
//		}
//	}

func SetupTable(name string) error {
	conf := aws.NewConfig().WithEndpoint("http://localhost:8000").WithRegion("us-west-1")
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: *conf,
	}))

	_, _ = os.Getwd()
	dynamodbSvc := dynamodb.New(sess)
	tableName := aws.String(name)
	input := &dynamodb.CreateTableInput{
		AttributeDefinitions: []*dynamodb.AttributeDefinition{
			{
				AttributeName: aws.String("LicenseName"),
				AttributeType: aws.String("S"),
			},
		},
		KeySchema: []*dynamodb.KeySchemaElement{
			{
				AttributeName: aws.String("LicenseName"),
				KeyType:       aws.String("HASH"),
			},
		},
		ProvisionedThroughput: &dynamodb.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(1),
			WriteCapacityUnits: aws.Int64(1),
		},
		TableName: tableName,
	}

	_, err := dynamodbSvc.CreateTable(input)

	// ok if table already exists
	if err != nil && err.(awserr.Error).Code() != "ResourceInUseException" {
		return nil
	}
	return err
}

func LoadTableFromFile(tableName string, fileName string) error {
	licenses := []License{}
	fmt.Println(os.Getwd())
	bytes, err := ioutil.ReadFile(fileName)
	if err != nil {
		return err
	} else {
		json.Unmarshal(bytes, &licenses)

		conf := aws.NewConfig().WithEndpoint("http://localhost:8000").WithRegion("us-west-1")
		sess := session.Must(session.NewSessionWithOptions(session.Options{
			Config: *conf,
		}))
		dynamodbSvc := dynamodb.New(sess)

		for _, license := range licenses {
			marshalledLicense, err := dynamodbattribute.MarshalMap(&license)
			if err != nil {
				return err
			}
			_, err = dynamodbSvc.PutItem(&dynamodb.PutItemInput{
				Item:      marshalledLicense,
				TableName: aws.String(tableName),
			})
			if err != nil {
				return err
			}
		}

		return nil
	}
}

func AddLicense(license License) error {
	conf := aws.NewConfig().WithEndpoint("http://localhost:8000").WithRegion("us-west-1")
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: *conf,
	}))

	dynamodbSvc := dynamodb.New(sess)
	tableName := aws.String("licenses-test")

	marshalledLicenseItem, _ := dynamodbattribute.MarshalMap(license)
	_, err := dynamodbSvc.PutItem(&dynamodb.PutItemInput{
		Item:      marshalledLicenseItem,
		TableName: tableName,
	})

	return err
}

func GetLicenses() ([]License, error) {

	conf := aws.NewConfig().WithEndpoint("http://localhost:8000").WithRegion("us-west-1")
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: *conf,
	}))

	dynamodbSvc := dynamodb.New(sess)
	scanInput := &dynamodb.ScanInput{
		TableName: aws.String(LICENSE_TABLE),
	}

	res, err := dynamodbSvc.Scan(scanInput)

	if err != nil {
		return []License{}, nil
	}
	var licenses []License

	for _, licenseItem := range res.Items {
		unmarshalledLicense := License{}
		dynamodbattribute.UnmarshalMap(licenseItem, &unmarshalledLicense)
		licenses = append(licenses, unmarshalledLicense)
	}
	return licenses, nil
}

func CheckoutLicense(licenseName string, user string) error {
	license, err := getLicense(licenseName)
	if err != nil {
		return err
	}

	if _, alreadyCheckedOut := license.LicenseUsers[user]; alreadyCheckedOut {
		return fmt.Errorf("user %s already has license %s", user, licenseName)
	} else {
		// retry if someone else modifies this license document while we attempt to

		for attempts := 0; attempts < MAX_CHECKOUT_ATTEMPTS; attempts++ {

			if len(license.LicenseUsers) == license.UserLimit {
				return fmt.Errorf("license %s is already at max user capacity", licenseName)
			}

			didSetUser, err := setUserLicenseIfNotStale(license, user)
			if didSetUser {
				fmt.Printf("license %s successfully checked out to user %s\n", licenseName, user)
				return nil
			} else if err != nil {
				return err
			}
			fmt.Printf(
				"stale read while attempting to checkout license %s to user %s. retrying...\n", licenseName, user,
			)

			license, err = getLicense(licenseName)
			if err != nil {
				return err
			}
		}
		return fmt.Errorf(
			"exceeded max attempts to checkout license %s to user %s. (high concurrent activity)", licenseName, user,
		)
	}
}

// renew a license to a user
func RenewLicense(licenseName string, user string) error {
	license, err := getLicense(licenseName)
	if err != nil {
		return err
	}

	// retry if someone else modifies this license document while we attempt to
	for attempts := 0; attempts < MAX_CHECKOUT_ATTEMPTS; attempts++ {

		if _, isCheckedOut := license.LicenseUsers[user]; !isCheckedOut {
			return fmt.Errorf("user %s has not checked out license %s", user, licenseName)
		}

		didSetUser, err := setUserLicenseIfNotStale(license, user)
		if didSetUser {
			fmt.Printf("license %s successfully renewed for user %s\n", licenseName, user)
			return nil
		} else if err != nil {
			return err
		}
		fmt.Printf(
			"stale read while attempting to renew license %s for user %s. retrying...", licenseName, user,
		)

		license, err = getLicense(licenseName)
		if err != nil {
			return err
		}
	}
	return fmt.Errorf(
		"exceeded max attempts to checkout license %s to user %s. (high concurrent activity)", licenseName, user,
	)
}

func RevokeLicense(licenseName string, user string) {

	license, _ := getLicense(licenseName)

	for attempts := 0; attempts < MAX_CHECKOUT_ATTEMPTS; attempts++ {
		if revokeUserLicenseIfNotStale(license, user) == nil {
			return
		}
	}
}

func getLicense(licenseName string) (License, error) {

	conf := aws.NewConfig().WithEndpoint("http://localhost:8000").WithRegion("us-west-1")
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: *conf,
	}))
	dynamodbSvc := dynamodb.New(sess)

	license := License{}
	filt := expression.Name("LicenseName").Equal(expression.Value(licenseName))
	expr, err := expression.NewBuilder().WithFilter(filt).Build()
	if err != nil {
		return license, err
	}

	scanInput := &dynamodb.ScanInput{
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
		FilterExpression:          expr.Filter(),
		TableName:                 aws.String(LICENSE_TABLE),
	}
	res, err := dynamodbSvc.Scan(scanInput)
	if err != nil {
		return license, err
	}

	if len(res.Items) == 0 {
		return license, fmt.Errorf("License %s not found", licenseName)
	}

	dynamodbattribute.UnmarshalMap(res.Items[0], &license)
	return license, nil
}

func setUserLicenseIfNotStale(license License, user string) (bool, error) {

	ensureCleanReadCondition := expression.Name("LicenseUsers").Equal(expression.Value(license.LicenseUsers))
	expr, err := expression.NewBuilder().
		WithCondition(ensureCleanReadCondition).
		Build()
	if err != nil {
		return false, err
	}

	conf := aws.NewConfig().WithEndpoint("http://localhost:8000").WithRegion("us-west-1")
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: *conf,
	}))
	dynamodbSvc := dynamodb.New(sess)

	if license.LicenseUsers == nil {
		license.LicenseUsers = make(map[string]int64)
	}
	license.LicenseUsers[user] = time.Now().Unix()
	marshalledLicenseItem, _ := dynamodbattribute.MarshalMap(license)

	_, err = dynamodbSvc.PutItem(&dynamodb.PutItemInput{
		Item:                      marshalledLicenseItem,
		TableName:                 aws.String(LICENSE_TABLE),
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})

	if err != nil {
		fmt.Printf("%v\n", err)
		if awsErr, _ := err.(awserr.Error); awsErr.Code() == "ConditionalCheckFailedException" {
			return false, nil
		} else {
			return false, err
		}
	} else {
		return true, nil
	}

}

func revokeUserLicenseIfNotStale(license License, user string) error {

	ensureCleanReadCondition := expression.Name("LicenseUsers").Equal(expression.Value(license.LicenseUsers))
	expr, err := expression.NewBuilder().
		WithCondition(ensureCleanReadCondition).
		Build()
	if err != nil {
		return err
	}

	conf := aws.NewConfig().WithEndpoint("http://localhost:8000").WithRegion("us-west-1")
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: *conf,
	}))
	dynamodbSvc := dynamodb.New(sess)

	delete(license.LicenseUsers, user)
	marshalledLicenseItem, _ := dynamodbattribute.MarshalMap(license)

	_, err = dynamodbSvc.PutItem(&dynamodb.PutItemInput{
		Item:                      marshalledLicenseItem,
		TableName:                 aws.String(LICENSE_TABLE),
		ConditionExpression:       expr.Condition(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})

	return err
}
