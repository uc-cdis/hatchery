package hatchery

import (
	"encoding/json"
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

	err := license.CheckoutToUser(initialUsername)
	if _, isMapped := license.Users[initialUsername]; !isMapped || nil != err {
		t.Errorf("Failed to checkout license to existing user. Checkout should be idempotent.")
	}

	for i := 0; i < license.UserLimit-1; i++ {
		userName := fmt.Sprintf("user%v@uchicago.edu", i+2)
		if nil != license.CheckoutToUser(userName) {
			t.Errorf("Failed to checkout license #%v", i+2)
		}
		if _, isMapped := license.Users[userName]; !isMapped {
			t.Errorf("Failed to map user %v", i)
		}
	}

	if nil == license.CheckoutToUser(fmt.Sprintf("user%v@uchicago.edu", license.UserLimit)) {
		t.Error("Incorrectly checked out license past user limit.")
	}

	for userName, user := range license.Users {
		// ignore initially stored test user's timestamp
		if userName != initialUsername && user.LastUsedTimestamp < startTime {
			t.Errorf("Incorrectly set lastUsedTimestamp on user %v, %v", user, startTime)
		}
	}

	if _, err := NewLicense(license.fileName); nil != err {
		t.Errorf("Error unmarshalling JSON after license checkout %v", err)
	}

}

func TestReleaseLicense(t *testing.T) {
	fileName := getTestLicenseFile()
	license, _ := NewLicense(fileName)
	defer os.Remove(fileName)

	addedUsername := "user2@uchicago.edu"
	license.CheckoutToUser(addedUsername)

	license.ReleaseFromUser(initialUsername)
	if _, isPresent := license.Users[initialUsername]; isPresent {
		t.Errorf("Failed to remove user %v from licenses", initialUsername)
	}

	if _, isPresent := license.Users[addedUsername]; !isPresent {
		t.Errorf("Incorrectly removed user %v from licenses", addedUsername)
	}
}

func TestConcurrentUsage(t *testing.T) {
	fileName := getTestLicenseFile()
	license, _ := NewLicense(fileName)
	defer os.Remove(fileName)

	// 5 second timeout to check for deadlock
	go func() {
		time.Sleep(time.Duration(5) * time.Second)
		t.Errorf("Timeout exceeded. Assuming deadlock.")
	}()

	type res struct {
		Success bool
		UserNum int
	}
	numUsers := 100
	checkoutResults := make(chan res, numUsers*2)

	checkoutLicense := func(userNum int) {
		user := fmt.Sprintf("user%v", userNum)
		if nil == license.CheckoutToUser(user) {
			numConcurrentUsers := len(license.Users)
			if numConcurrentUsers > license.UserLimit {
				t.Errorf(
					"Checked out license to %v users, exceeding %v\n", numConcurrentUsers, license.UserLimit,
				)
			}
			if _, isRecorded := license.Users[user]; !isRecorded {
				t.Errorf("Checkout succeeded, but user %v not mapped to license: %v", userNum, license.Users)
			}

			time.Sleep(time.Duration(1) * time.Millisecond)

			license.ReleaseFromUser(user)
			if _, isRecorded := license.Users[user]; isRecorded {
				t.Errorf("License was released, but user %v still mapped to license: %v", userNum, license.Users)
			}
			checkoutResults <- res{Success: true, UserNum: userNum}
		}
		checkoutResults <- res{Success: false, UserNum: userNum}
	}

	// everyone tries to check out a license
	for i := 0; i < numUsers; i++ {
		checkoutLicense(i)
	}

	// // all failed checkouts retry until everyone has acquired and released their license
	for numCompleted := 0; numCompleted < numUsers+100; {
		result := <-checkoutResults
		if result.Success {
			numCompleted++
		} else {
			checkoutLicense(result.UserNum)
		}
	}

	if len(license.Users) != 1 {
		t.Errorf("After many concurrent ops, initial user not present%v", license.Users)
	}

	testFile := getTestLicenseFile()
	defer os.Remove(testFile)
	expected, _ := NewLicense(testFile)
	expectedJSON, _ := json.Marshal(expected)
	actualJSON, _ := json.Marshal(license)

	if string(expectedJSON) != string(actualJSON) {
		t.Errorf("After many concurrent r/w, expected %v, got %v", expectedJSON, actualJSON)
	}
}
