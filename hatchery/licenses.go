package hatchery

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
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
	logger   *log.Logger
}

type User struct {
	LicenseName       string `json:"licenseName"`
	LastUsedTimestamp int64  `json:"lastUsedTimestamp"`
}

type Transaction struct {
	UserName    string
	Context     context.Context
	AccessToken string
	Action      TransactionType
	Result      chan TransactionResult
}

type TransactionType int

const (
	ADD_USER TransactionType = iota
	RENEW_USER
	REMOVE_EXPIRED_USERS
)

type TransactionResult struct {
	Error error
	Users map[string]User
}

const (
	TIMEOUT_SECONDS = 60
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

	license.logger = log.New(os.Stdout, fmt.Sprintf("[%v]", license.Name), log.LstdFlags)
	license.logger.Printf("Initialized from file %v", license.fileName)

	go license.processTransactions()
	return &license, nil
}

func (license *License) processTransactions() {
	var err error
	for tx := range license.updates {

		switch tx.Action {
		case ADD_USER:
			{
				if _, alreadyCheckedOut := license.Users[tx.UserName]; !alreadyCheckedOut {
					if len(license.Users) < license.UserLimit {
						license.Users[tx.UserName] = User{
							LicenseName:       license.Name,
							LastUsedTimestamp: time.Now().Unix(),
						}
						license.logger.Printf("Checked out license to %v", tx.UserName)
					} else {
						err = fmt.Errorf("License is already at user limit")
					}
				}
			}
		case RENEW_USER:
			{
				if _, userExists := license.Users[tx.UserName]; userExists {
					license.Users[tx.UserName] = User{
						LicenseName:       license.Name,
						LastUsedTimestamp: time.Now().Unix(),
					}
					license.logger.Printf("Renewed for user %v", tx.UserName)
				} else {
					err = fmt.Errorf("User does not have this license")
				}
			}
		case REMOVE_EXPIRED_USERS:
			{
				for userName, user := range license.Users {
					if time.Now().Unix() > user.LastUsedTimestamp+TIMEOUT_SECONDS {
						delete(license.Users, userName)
						license.logger.Printf("License expired for %v", userName)
					}
				}
			}
		}
		license.marshal()
		if nil != tx.Result {
			usersCopy := make(map[string]User)
			for userName, user := range license.Users {
				usersCopy[userName] = user
			}
			tx.Result <- TransactionResult{Error: err, Users: usersCopy}
		}
	}
}

func (license *License) CheckoutToUser(userName string) (map[string]User, error) {

	transaction := &Transaction{
		UserName: userName,
		Action:   ADD_USER,
		Result:   make(chan TransactionResult),
	}
	license.updates <- transaction

	res := <-transaction.Result
	return res.Users, res.Error
}

func (license *License) RenewForUser(userName string) (map[string]User, error) {
	transaction := &Transaction{
		UserName: userName,
		Action:   RENEW_USER,
		Result:   make(chan TransactionResult),
	}
	license.updates <- transaction

	res := <-transaction.Result
	return res.Users, res.Error
}

func MonitorConfiguredLicensesForExpiry() {
	for {
		time.Sleep(time.Duration(30) * time.Second)
		for _, license := range Config.Licenses {
			transaction := &Transaction{
				Action: REMOVE_EXPIRED_USERS,
			}
			license.updates <- transaction
		}
	}
}

func RenewAllLicensesForUser(userName string) {
	for _, license := range Config.Licenses {
		license.RenewForUser(userName)
	}
}

func (license *License) marshal() {
	bytes, _ := json.MarshalIndent(license, "", "\t")
	ioutil.WriteFile(license.fileName, bytes, 0644)
}
