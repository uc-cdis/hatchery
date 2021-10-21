package hatchery

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"sync"
	"time"
)

type License struct {
	Name        string          `json:"name"`
	UserLimit   int             `json:"userLimit"`
	LicenseData string          `json:"licenseData"`
	Users       map[string]User `json:"users"`
	// not marshalled
	fileName string           `json:"-"`
	mutex    sync.Mutex       `json:"-"`
	updates  chan Transaction `json:"-"`
}

type Transaction struct {
	UserName string
	Action   Update
}

type Update int

const (
	ADD_USER Update = iota
	REMOVE_USER
	CHECK_USER
)

type User struct {
	LicenseName       string `json:"licenseName"`
	LastUsedTimestamp int64  `json:"lastUsedTimestamp"`
}

const (
	TIMEOUT_SECONDS = 30
)

func NewLicense(licenseFilePath string) (*License, error) {

	var license License
	licenseData, err := ioutil.ReadFile(licenseFilePath)
	if nil != err {
		return &license, err
	}

	err = json.Unmarshal(licenseData, &license)
	if nil != err {
		return &license, err
	}
	license.fileName = licenseFilePath
	return &license, nil
}

func (license *License) CheckoutToUser(userName string) error {

	license.mutex.Lock()
	defer license.mutex.Unlock()

	if _, alreadyCheckedOut := license.Users[userName]; alreadyCheckedOut {
		return fmt.Errorf("%v aleady has license checked out", userName)
	}

	if len(license.Users) < license.UserLimit {
		checkoutTimestamp := time.Now().Unix()
		license.Users[userName] = User{
			LicenseName:       license.Name,
			LastUsedTimestamp: checkoutTimestamp,
		}
		license.marshal()
		return nil
	} else {
		return errors.New("License is already at userlimit")
	}
}

func (license *License) ReleaseFromUser(userName string) {

	license.mutex.Lock()
	defer license.mutex.Unlock()

	delete(license.Users, userName)
	license.marshal()
}

func (license *License) GetUsers() map[string]User {
	// Users tag must be exported for json marshalling
	// but should not be read directly
	// in order to avoid concurrent map read / write
	license.mutex.Lock()
	defer license.mutex.Unlock()
	usersCopy := make(map[string]User)
	for k, v := range license.Users {
		usersCopy[k] = v
	}
	return usersCopy
}

func (license *License) StartMonitoringUserPods() {
	for {
		time.Sleep(time.Duration(30) * time.Second)
		for userName, user := range license.GetUsers() {
			workspaceStatus, err := statusK8sPod(userName)
			if nil != err {
				// revoke license ?
			} else if workspaceStatus.Status == "Launching" || workspaceStatus.Status == "Running" {
				user.LastUsedTimestamp = time.Now().Unix()
				license.Users[userName] = user
			} else {
				license.ReleaseFromUser(userName)
			}
		}
	}
}

func (license *License) marshal() {
	bytes, _ := json.MarshalIndent(license, "", "\t")
	ioutil.WriteFile(license.fileName, bytes, 0644)
}
