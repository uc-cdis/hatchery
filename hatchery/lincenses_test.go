package hatchery

import (
	"log"
	"testing"
	"time"

	// AWS
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"

	// monkey patch
	"github.com/undefinedlabs/go-mpatch"
)

func setupTable() {
	conf := aws.NewConfig().WithEndpoint("http://localhost:8000").WithRegion("us-west-1")
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: *conf,
	}))

	dynamodbSvc := dynamodb.New(sess)
	tableName := aws.String("licenses-test")
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
			ReadCapacityUnits:  aws.Int64(10),
			WriteCapacityUnits: aws.Int64(10),
		},
		TableName: tableName,
	}
	_, err := dynamodbSvc.CreateTable(input)

	// ok if table already exists
	if err != nil && err.(awserr.Error).Code() != "ResourceInUseException" {
		log.Fatalf("Got error calling CreateTable: %s", err)
	}

	license := License{
		LicenseName:  "STATA-HEAL",
		UserLimit:    6,
		LicenseData:  "abcdefg1234$$$",
		LicenseUsers: make(map[string]int64),
	}

	marshalledLicenseItem, _ := dynamodbattribute.MarshalMap(license)
	_, err = dynamodbSvc.PutItem(&dynamodb.PutItemInput{
		Item:      marshalledLicenseItem,
		TableName: tableName,
	})
	if err != nil {
		log.Fatalf("Got error calling PutItem: %s", err)
	}
}

func TestGetLicenses(t *testing.T) {
	setupTable()
	licenses, _ := GetLicenses()
	if len(licenses) != 1 {
		t.Errorf("Expected one license, got %v", licenses)
	}
}

func TestCheckoutLicense(t *testing.T) {
	setupTable()
	err := CheckoutLicense("STATA-HEAL", "someUser")
	if err != nil {
		t.Error(err)
	}

	licenses, _ := GetLicenses()
	if len(licenses[0].LicenseUsers) != 1 {
		t.Errorf("Expected license checked out to one user")
	}

	err = CheckoutLicense("STATA-HEAL", "someUser")
	if err == nil {
		t.Error("Should fail to re-checkout license to user")
	}

	err = CheckoutLicense("STATA-HEAL", "newUser")
	licenses, _ = GetLicenses()
	if err != nil {
		t.Error(err)
	} else if len(licenses[0].LicenseUsers) != 2 {
		t.Error("failed to check out to new user")
	}

	for _, user := range []string{"user3", "user4", "user5", "user6"} {
		err = CheckoutLicense("STATA-HEAL", user)
		if err != nil {
			t.Error(err)
		}
	}

	err = CheckoutLicense("STATA-HEAL", "user7")
	if err == nil {
		t.Error("Should fail to checkout license to user past license UserLimit")
	}

	err = RenewLicense("nonExistantLicense", "someUser")
	if err == nil {
		t.Errorf("Should fail to checkout nonexistant license")
	}
}

func TestRenewLicense(t *testing.T) {
	setupTable()
	_ = CheckoutLicense("STATA-HEAL", "someUser")

	timeOfRenewal := time.Now().Add(time.Second * 30)
	mpatch.PatchMethod(time.Now, func() time.Time { return timeOfRenewal })

	err := RenewLicense("STATA-HEAL", "someUser")
	if err != nil {
		t.Error(err)
	}

	licenses, _ := GetLicenses()
	license := licenses[0]
	if len(license.LicenseUsers) != 1 {
		t.Errorf("Renewing license should not change length of user list")
	}

	if license.LicenseUsers["someUser"] != timeOfRenewal.Unix() {
		t.Errorf("Renewing license should update user timestamp to now")
	}

	err = RenewLicense("STATA-HEAL", "nonExistantUser")
	if err == nil {
		t.Errorf("Should fail to renew license to user with none checked out")
	}

	err = RenewLicense("nonExistantLicense", "someUser")
	if err == nil {
		t.Errorf("Should fail to renew nonexistant license")
	}
}

func TestRevokeLicense(t *testing.T) {
	setupTable()
	_ = CheckoutLicense("STATA-HEAL", "someUser")
	RevokeLicense("STATA-HEAL", "someUser")

	licenses, _ := GetLicenses()
	license := licenses[0]

	if len(license.LicenseUsers) != 0 {
		t.Errorf("Revoking license should remove user from list")
	}

	_ = CheckoutLicense("STATA-HEAL", "someUser")
	licenses, _ = GetLicenses()
	license = licenses[0]

	if len(license.LicenseUsers) != 1 {
		t.Errorf("User should be able to check out license after revoking")
	}

}
