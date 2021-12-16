package hatchery

import (
	"testing"
	"time"

	// AWS
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"

	// monkey patch
	"github.com/undefinedlabs/go-mpatch"
)

func resetTable() {
	conf := aws.NewConfig().WithEndpoint("http://localhost:8000").WithRegion("us-west-1")
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		Config: *conf,
	}))

	tableName := "licenses-test"
	dynamodbSvc := dynamodb.New(sess)
	dynamodbSvc.DeleteTable(&dynamodb.DeleteTableInput{
		TableName: aws.String(tableName),
	})
	SetupTable(tableName)
	err := LoadTableFromFile(tableName, "../testData/testLicenses.json")
	if err != nil {
		panic(err)
	}
	// marshalledLicense, _ := dynamodbattribute.MarshalMap(&License{
	// 	LicenseName:  "STATA-HEAL",
	// 	LicenseData:  "abcdefg123$$$",
	// 	LicenseUsers: map[string]int64{},
	// 	UserLimit:    6,
	// })
	// dynamodbSvc.PutItem(&dynamodb.PutItemInput{
	// 	Item:      marshalledLicense,
	// 	TableName: aws.String("licenses-test"),
	// })
}

func TestGetLicenses(t *testing.T) {
	resetTable()
	licenses, _ := GetLicenses()
	if len(licenses) != 1 {
		t.Errorf("Expected one license, got %v", licenses)
	}
}

func TestCheckoutLicense(t *testing.T) {
	resetTable()
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
	resetTable()
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
	resetTable()
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
