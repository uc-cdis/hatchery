package hatchery

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"time"
)

type License struct {
	Name        string          `json:"name"`
	UserLimit   int             `json:"userLimit"`
	LicenseData string          `json:"licenseData"`
	Users       map[string]User `json:"users"`
	// not marshalled
	fileName string            `json:"-"`
	updates  chan *Transaction `json:"-"`
}

type Transaction struct {
	UserName string
	Action   TransactionRequest
	Result   chan error
}

type TransactionRequest int

const (
	REQUEST_ADD_USER TransactionRequest = iota
	REQUEST_REMOVE_USER
	REQUEST_REMOVE_EXPIRED_USERS
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
	license.updates = make(chan *Transaction, 100)

	go license.processTransactions()
	go license.monitorUserPods()
	return &license, nil
}

func (license *License) processTransactions() {
	for {
		tx := <-license.updates
		var err error

		switch tx.Action {
		case REQUEST_ADD_USER:
			{
				if _, alreadyCheckedOut := license.Users[tx.UserName]; !alreadyCheckedOut {
					if len(license.Users) < license.UserLimit {
						license.Users[tx.UserName] = User{
							LicenseName:       license.Name,
							LastUsedTimestamp: time.Now().Unix(),
						}
					} else {
						err = fmt.Errorf("License %v is already at user limit", license.Name)
					}
				}
			}
		case REQUEST_REMOVE_USER:
			{
				delete(license.Users, tx.UserName)
			}
		case REQUEST_REMOVE_EXPIRED_USERS:
			{
				for userName, user := range license.Users {
					userWorkspaceStatus, _ := statusK8sPod(userName)
					if userWorkspaceStatus.Status == "Running" || userWorkspaceStatus.Status == "Launching" {
						user.LastUsedTimestamp = time.Now().Unix()
						license.Users[tx.UserName] = user
					} else if user.LastUsedTimestamp+60 < time.Now().Unix() {
						license.ReleaseFromUser(userName)
					}
				}
			}
		}
		license.marshal()
		tx.Result <- err
	}
}

func (license *License) CheckoutToUser(userName string) error {

	transaction := &Transaction{
		UserName: userName,
		Action:   REQUEST_ADD_USER,
		Result:   make(chan error),
	}
	license.updates <- transaction

	err := <-transaction.Result
	return err

}

func (license *License) ReleaseFromUser(userName string) {

	transaction := &Transaction{
		UserName: userName,
		Action:   REQUEST_REMOVE_USER,
		Result:   make(chan error),
	}
	license.updates <- transaction

	<-transaction.Result
}

func (license *License) monitorUserPods() {
	for {
		time.Sleep(time.Duration(30) * time.Second)
		transaction := &Transaction{
			Action: REQUEST_REMOVE_EXPIRED_USERS,
			Result: make(chan error),
		}
		license.updates <- transaction
	}
}

func (license *License) marshal() {
	bytes, _ := json.MarshalIndent(license, "", "\t")
	ioutil.WriteFile(license.fileName, bytes, 0644)
}
