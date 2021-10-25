package hatchery

import (
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"testing"
	"time"
)

const (
	initialUsername = "user@uchicago.edu"
)

func getTestLicenseFile() string {
	testDataJSON, _ := ioutil.ReadFile("../testData/licenses/testLicenses.json")

	tempFile, _ := ioutil.TempFile("../testData/licenses", "")
	defer tempFile.Close()

	ioutil.WriteFile(tempFile.Name(), testDataJSON, 0644)
	return tempFile.Name()
}

func TestLoadLicenses(t *testing.T) {
	fileName := getTestLicenseFile()
	license, err := NewLicense(fileName)
	defer os.Remove(fileName)

	if nil != err {
		t.Error(fmt.Sprintf("failed to load license statuses, got: %v", err))
		return
	}

	expectedUsers := map[string]User{
		initialUsername: {
			LicenseName:       "Stata-HEAL",
			LastUsedTimestamp: 1634590864,
		},
	}

	if license.Name != "Stata-HEAL" ||
		license.UserLimit != 6 ||
		license.LicenseData != "Stata-license-info" ||
		!reflect.DeepEqual(license.Users, expectedUsers) {
		t.Error("Failed to parse expected license data")
	}
}

func TestCheckoutLicense(t *testing.T) {
	fileName := getTestLicenseFile()
	license, _ := NewLicense(fileName)
	defer os.Remove(fileName)

	startTime := time.Now().Unix()

	updatedUsers, err := license.CheckoutToUser(initialUsername)
	if _, isMapped := updatedUsers[initialUsername]; !isMapped || nil != err {
		t.Errorf("Failed to checkout license to existing user. Checkout should be idempotent.")
	}

	for i := 2; i <= license.UserLimit; i++ {
		userName := fmt.Sprintf("user%v@uchicago.edu", i)
		updatedUsers, err = license.CheckoutToUser(userName)
		if nil != err {
			t.Errorf("Failed to checkout license #%v", i)
		}
		if _, isMapped := updatedUsers[userName]; !isMapped {
			t.Errorf("Failed to map user %v", i)
		}
	}

	overLimitUser := fmt.Sprintf("user%v@uchicago.edu", license.UserLimit+1)
	updatedUsers, err = license.CheckoutToUser(overLimitUser)
	if _, isMapped := updatedUsers[overLimitUser]; isMapped || nil == err {
		t.Errorf("Incorrectly checked out license past user limit. %v", updatedUsers)
	}

	for userName, user := range updatedUsers {
		// ignore initially stored test user's timestamp
		if userName != initialUsername && user.LastUsedTimestamp < startTime {
			t.Errorf("Incorrectly set lastUsedTimestamp on user %v, %v", user, startTime)
		}
	}

	// license should have been marshalled into a state from which we can build a new license
	if _, err := NewLicense(license.fileName); nil != err {
		t.Errorf("Error unmarshalling JSON after license checkout %v", err)
	}
}

func TestRenewLicense(t *testing.T) {
	fileName := getTestLicenseFile()
	license, _ := NewLicense(fileName)
	defer os.Remove(fileName)

	startTime := time.Now().Unix()
	time.Sleep(time.Duration(1) * time.Second)
	users, err := license.RenewForUser(initialUsername)
	if users[initialUsername].LastUsedTimestamp <= startTime || nil != err {
		t.Errorf("Failed to renew license for user")
	}

	_, err = license.RenewForUser("nonexistantuser")
	if nil == err {
		t.Errorf("Should not be able to renew license for non-checked out user")
	}
}
