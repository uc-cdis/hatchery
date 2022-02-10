package hatchery

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"time"

	// AWS
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/aws/aws-sdk-go/service/dynamodb/expression"
)

type License struct {
	LicenseName  string
	UserLimit    int
	LicenseData  string
	LicenseUsers map[string]int64
}

func (l License) MarshalJSON() ([]byte, error) {
	return []byte(
		fmt.Sprintf(
			"{\"name\": \"%s\", \"userLimit\": %v, \"inUse\": %v}", l.LicenseName, l.UserLimit, len(l.LicenseUsers),
		),
	), nil
}

const (
	LICENSE_TIMEOUT_SECONDS  = 60
	MAX_SET_LICENSE_ATTEMPTS = 5
	LICENSE_TABLE            = "licenses-test"
)

func SetupLicensesTable() error {

	Config.Logger.Printf("Attempting to setup licenses table %s\n", Config.Config.LicensesDynamodbTable)
	dynamodbSvc := GetDynamoDBSVC()

	tableName := aws.String(Config.Config.LicensesDynamodbTable)
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
	if err != nil {
		if err.(awserr.Error).Code() == "ResourceInUseException" {
			Config.Logger.Printf("Licenses table %s already exists.\n", Config.Config.LicensesDynamodbTable)
			return nil
		} else {
			Config.Logger.Printf("Error setting up table %s: %v", Config.Config.LicensesDynamodbTable, err)
		}
	} else {
		Config.Logger.Printf("Successfully setup table %s\n", Config.Config.LicensesDynamodbTable)
	}
	return err
}

func LoadLicensesTableFromFile(fileName string) error {
	licenses := []License{}

	bytes, err := ioutil.ReadFile(fileName)
	if err != nil {
		return err
	} else {
		err = json.Unmarshal(bytes, &licenses)
		if err != nil {
			return err
		}
		dynamodbSvc := GetDynamoDBSVC()
		for _, license := range licenses {
			marshalledLicense, err := dynamodbattribute.MarshalMap(&license)
			if err != nil {
				return err
			}
			_, err = dynamodbSvc.PutItem(&dynamodb.PutItemInput{
				Item:      marshalledLicense,
				TableName: aws.String(Config.Config.LicensesDynamodbTable),
			})
			if err != nil {
				return err
			}
		}

		return nil
	}
}

func AddLicense(license License) error {

	dynamodbSvc := GetDynamoDBSVC()
	tableName := aws.String("licenses-test")

	marshalledLicenseItem, _ := dynamodbattribute.MarshalMap(license)
	_, err := dynamodbSvc.PutItem(&dynamodb.PutItemInput{
		Item:      marshalledLicenseItem,
		TableName: tableName,
	})

	return err
}

func GetLicenses() ([]License, error) {

	dynamodbSvc := GetDynamoDBSVC()
	scanInput := &dynamodb.ScanInput{
		TableName: aws.String(LICENSE_TABLE),
	}

	res, err := dynamodbSvc.Scan(scanInput)

	if err != nil {
		return []License{}, err
	}
	var licenses []License

	for _, licenseItem := range res.Items {
		unmarshalledLicense := License{}
		err = dynamodbattribute.UnmarshalMap(licenseItem, &unmarshalledLicense)
		if err != nil {
			return []License{}, err
		}
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

		for attempts := 0; attempts < MAX_SET_LICENSE_ATTEMPTS; attempts++ {

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
	for attempts := 0; attempts < MAX_SET_LICENSE_ATTEMPTS; attempts++ {

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

func RevokeExpiredLicenses() {
	for range time.Tick(time.Second * LICENSE_TIMEOUT_SECONDS) {
		licenses, err := GetLicenses()
		if err == nil {
			for _, license := range licenses {
				now := time.Now().Unix()
				for user, timestamp := range license.LicenseUsers {
					if timestamp <= now-LICENSE_TIMEOUT_SECONDS {
						RevokeLicense(license.LicenseName, user)
					}
				}
			}
		}
	}
}

func RevokeLicense(licenseName string, user string) {

	license, _ := getLicense(licenseName)

	for attempts := 0; attempts < MAX_SET_LICENSE_ATTEMPTS; attempts++ {
		if revokeUserLicenseIfNotStale(license, user) == nil {
			return
		}
	}
}

func getLicense(licenseName string) (License, error) {

	dynamodbSvc := GetDynamoDBSVC()

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

	err = dynamodbattribute.UnmarshalMap(res.Items[0], &license)
	if err != nil {
		return license, err
	}
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

	dynamodbSvc := GetDynamoDBSVC()

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

	dynamodbSvc := GetDynamoDBSVC()

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
