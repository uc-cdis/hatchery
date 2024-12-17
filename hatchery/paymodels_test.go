package hatchery

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
)

func TestGetDefaultPayModel(t *testing.T) {
	defer SetupAndTeardownTest()()

	defaultPayModelForTest := &PayModel{
		Name:  "Trial Workspace",
		Local: true,
	}

	configWithPayModel := &FullHatcheryConfig{
		Config: HatcheryConfig{
			DefaultPayModel: *defaultPayModelForTest,
		},
	}

	/* Setup */
	Config = configWithPayModel

	/* Act */
	got, err := getDefaultPayModel()

	/* Assert */
	if err != nil {
		t.Errorf("Unexpected error. Should be nil but got %v", err)
	}
	if !reflect.DeepEqual(got, defaultPayModelForTest) {
		t.Errorf("Unexpected output: \nWant:\n\t %+v,\n Got:\n\t %+v",
			defaultPayModelForTest,
			got)
	}

}

func Test_GetCurrentPayModel(t *testing.T) {
	defer SetupAndTeardownTest()()

	configWithDbTable := &FullHatcheryConfig{
		Config: HatcheryConfig{
			PayModelsDynamodbTable: "random_non_empty_string",
		},
	}

	configWithNoDbTable := &FullHatcheryConfig{
		Config: HatcheryConfig{
			PayModelsDynamodbTable: "",
		},
	}

	defaultPayModelForTest := &PayModel{
		Name:  "Trial Workspace",
		Local: true,
	}
	testCases := []struct {
		name                      string
		want                      *PayModel
		mockConfig                *FullHatcheryConfig
		mockDefaultPaymodel       *PayModel
		mockCurrentPayModelFromDB []PayModel
		mockPayModelsFromDB       []PayModel
	}{
		{
			name:                      "NoDB",
			want:                      defaultPayModelForTest,
			mockConfig:                configWithNoDbTable,
			mockCurrentPayModelFromDB: nil,
			mockPayModelsFromDB:       nil,
			mockDefaultPaymodel:       defaultPayModelForTest,
		},
		{
			name:                      "NoDB_NoDefaultPaymodel",
			want:                      nil,
			mockConfig:                configWithNoDbTable,
			mockCurrentPayModelFromDB: nil,
			mockPayModelsFromDB:       nil,
			mockDefaultPaymodel:       nil,
		},
		{
			name: "CurrentPayModelExists",
			want: &PayModel{
				Id:              "#1",
				Name:            "Direct Pay",
				CurrentPayModel: true,
				Status:          "active",
			},
			mockConfig: configWithDbTable,
			mockCurrentPayModelFromDB: []PayModel{
				{
					Id:              "#1",
					Name:            "Direct Pay",
					CurrentPayModel: true,
					Status:          "active",
				},
			},
			mockPayModelsFromDB: []PayModel{
				{
					Id:              "#1",
					Name:            "Direct Pay",
					CurrentPayModel: true,
					Status:          "active",
				},
				{
					Id:              "#2",
					Name:            "Direct Pay",
					CurrentPayModel: false,
					Status:          "active",
				},
			},
			mockDefaultPaymodel: nil,
		},
		{
			name:                      "ActiveButNotCurrentPaymodelExists",
			want:                      nil,
			mockConfig:                configWithDbTable,
			mockCurrentPayModelFromDB: []PayModel{},
			mockPayModelsFromDB: []PayModel{
				{
					Id:              "#1",
					Name:            "Direct Pay",
					CurrentPayModel: false,
					Status:          "active",
				},
				{
					Id:              "#2",
					Name:            "Direct Pay",
					CurrentPayModel: false,
					Status:          "active",
				},
			},
			mockDefaultPaymodel: nil,
		},
		{
			name:                      "NeitherCurrentNorActivePaymodelExists",
			want:                      defaultPayModelForTest,
			mockConfig:                configWithDbTable,
			mockCurrentPayModelFromDB: []PayModel{},
			mockPayModelsFromDB:       []PayModel{},
			mockDefaultPaymodel:       defaultPayModelForTest,
		},
	}

	for _, testcase := range testCases {
		t.Logf("Testing GetCurrentPaymodel when %s", testcase.name)
		/* Setup */
		Config = testcase.mockConfig
		getDefaultPayModel = func() (*PayModel, error) {
			return testcase.mockDefaultPaymodel, nil
		}
		payModelsFromDatabase = func(userName string, current bool) (payModels *[]PayModel, err error) {
			if current {
				return &testcase.mockCurrentPayModelFromDB, nil
			}
			return &testcase.mockPayModelsFromDB, nil
		}

		/* Act */
		got, err := getCurrentPayModel("testUser")
		if nil != err {
			t.Errorf("failed to load current pay model, got: %v", err)
			return
		}

		/* Assert */
		if testcase.want == nil {
			if got != nil {
				t.Errorf("\nassertion error while testing `GetPayModelsForUser` when %s : \nWant: %+v\nGot:%+v", testcase.name, testcase.want, got)
			}
		} else if !reflect.DeepEqual(got, testcase.want) {
			t.Errorf("\nassertion error while testing `GetCurrentPayModel` when %s : \nWant:%+v\nGot:%+v", testcase.name, testcase.want, got)
		}
	}
}
func Test_GetPayModelsForUser(t *testing.T) {
	defer SetupAndTeardownTest()()

	configWithDbTable := &FullHatcheryConfig{
		Config: HatcheryConfig{
			PayModelsDynamodbTable: "random_non_empty_string",
		},
	}

	configWithNoDbTable := &FullHatcheryConfig{
		Config: HatcheryConfig{
			PayModelsDynamodbTable: "",
		},
	}

	defaultPayModelForTest := &PayModel{
		Name:  "Trial Workspace",
		Local: true,
	}
	testCases := []struct {
		name                string
		want                *AllPayModels
		mockConfig          *FullHatcheryConfig
		mockCurrentPayModel *PayModel
		mockPayModelsFromDB []PayModel
	}{
		{
			name: "NoDB",
			want: &AllPayModels{
				CurrentPayModel: defaultPayModelForTest,
				PayModels: []PayModel{
					*defaultPayModelForTest,
				},
			},
			mockConfig:          configWithNoDbTable,
			mockCurrentPayModel: defaultPayModelForTest,
			mockPayModelsFromDB: nil,
		},
		{
			name:                "NoDB_NoDefaultPaymodel",
			want:                nil,
			mockConfig:          configWithNoDbTable,
			mockCurrentPayModel: nil,
			mockPayModelsFromDB: nil,
		},
		{
			name: "CurrentPayModelExists",
			want: &AllPayModels{
				CurrentPayModel: &PayModel{
					Id:              "#1",
					Name:            "Direct Pay",
					CurrentPayModel: true,
					Status:          "active",
				},
				PayModels: []PayModel{
					{
						Id:              "#1",
						Name:            "Direct Pay",
						CurrentPayModel: true,
						Status:          "active",
					},
					{
						Id:              "#2",
						Name:            "Direct Pay",
						CurrentPayModel: false,
						Status:          "active",
					},
				},
			},
			mockConfig: configWithDbTable,
			mockCurrentPayModel: &PayModel{
				Id:              "#1",
				Name:            "Direct Pay",
				CurrentPayModel: true,
				Status:          "active",
			},
			mockPayModelsFromDB: []PayModel{
				{
					Id:              "#1",
					Name:            "Direct Pay",
					CurrentPayModel: true,
					Status:          "active",
				},
				{
					Id:              "#2",
					Name:            "Direct Pay",
					CurrentPayModel: false,
					Status:          "active",
				},
			},
		},
		{
			name: "ActiveButNotCurrentPaymodelExists",
			want: &AllPayModels{
				CurrentPayModel: nil,
				PayModels: []PayModel{
					{
						Id:              "#1",
						Name:            "Direct Pay",
						CurrentPayModel: false,
						Status:          "active",
					},
					{
						Id:              "#2",
						Name:            "Direct Pay",
						CurrentPayModel: false,
						Status:          "active",
					},
				},
			},
			mockConfig:          configWithDbTable,
			mockCurrentPayModel: nil,
			mockPayModelsFromDB: []PayModel{
				{
					Id:              "#1",
					Name:            "Direct Pay",
					CurrentPayModel: false,
					Status:          "active",
				},
				{
					Id:              "#2",
					Name:            "Direct Pay",
					CurrentPayModel: false,
					Status:          "active",
				},
			},
		},
		{
			name: "NoActivePaymodelsExists",
			want: &AllPayModels{
				CurrentPayModel: defaultPayModelForTest,
				PayModels: []PayModel{
					*defaultPayModelForTest,
				},
			},
			mockConfig:          configWithDbTable,
			mockCurrentPayModel: defaultPayModelForTest,
			mockPayModelsFromDB: []PayModel{},
		},
	}

	for _, testcase := range testCases {
		t.Logf("Testing getPayModelsForUser when %s", testcase.name)

		/* Setup */
		Config = testcase.mockConfig
		getCurrentPayModel = func(username string) (*PayModel, error) {
			return testcase.mockCurrentPayModel, nil
		}
		payModelsFromDatabase = func(userName string, current bool) (payModels *[]PayModel, err error) {
			return &testcase.mockPayModelsFromDB, nil
		}

		/* Act */
		got, err := getPayModelsForUser("testUser")
		if nil != err {
			t.Errorf("failed to load pay models for user, got: %v", err)
			return
		}

		/* Assert */
		if testcase.want == nil {
			if got != nil {
				t.Errorf("\nassertion error while testing `GetPayModelsForUser` when %s : \nWant: %+v\nGot:%+v", testcase.name, testcase.want, got)
			}
		} else if !reflect.DeepEqual(got, testcase.want) {
			t.Errorf("\nassertion error while testing `GetPayModelsForUser` when %s : \nWant:\n\tCurrentPayModel: %+v,\n\tPaymodels %+v\nGot:\n\tCurrentPayModel: %+v,\n\tPaymodels %+v",
				testcase.name, testcase.want.CurrentPayModel, testcase.want.PayModels, got.CurrentPayModel, got.PayModels)
		}
	}
}

func Test_GetPayModelTableCreds(t *testing.T) {
	defer SetupAndTeardownTest()()

	configWithDbTable := &FullHatcheryConfig{
		Config: HatcheryConfig{
			PayModelsDynamodbTable: "random_non_empty_string",
		},
	}

	testCases := []struct {
		name       string
		want       aws.Config
		mockConfig *FullHatcheryConfig
	}{
		{
			name:       "PayModelTableInConfig",
			want:       aws.Config{},
			mockConfig: configWithDbTable,
		},
	}

	for _, testcase := range testCases {
		t.Logf("Testing getPayModelTableCreds when %s", testcase.name)

		/* Setup */
		Config = testcase.mockConfig
		//
		sess := session.Must(session.NewSessionWithOptions(session.Options{
			Config: aws.Config{
				Region: aws.String("us-east-1"),
			},
		}))

		/* Act */
		got := getPayModelTableCreds(sess)

		/* Assert */
		if !reflect.DeepEqual(got, testcase.want) {
			t.Errorf("\nassertion error while testing `GetPayModelTableCreds` when %s : \nWant:\n\taws.Config: %+v,\nGot:\n\taws.Config: %+v",
				testcase.name, testcase.want, got)
		}

		// Credentials should be nil
		if got.Credentials != nil {
			t.Errorf("\nassertion error while testing `GetPayModelTableCreds` when %s : \ngot.Credentials should be nil. \nGot: %+v", testcase.name, got.Credentials)
		}
	}
}

func Test_GetPayModelTableCredsWithArn(t *testing.T) {
	defer SetupAndTeardownTest()()

	configWithDbTableAndArn := &FullHatcheryConfig{
		Config: HatcheryConfig{
			PayModelsDynamodbTable: "random_non_empty_string",
			PayModelsDynamodbArn:   `arn:aws:iam::12345:role/other-role`,
		},
	}

	testCases := []struct {
		name       string
		mockConfig *FullHatcheryConfig
	}{
		{
			name:       "PayModelTableAndArnInConfig",
			mockConfig: configWithDbTableAndArn,
		},
	}

	for _, testcase := range testCases {
		t.Logf("Testing getPayModelTableCreds when %s", testcase.name)

		/* Setup */
		Config = testcase.mockConfig
		sess := session.Must(session.NewSessionWithOptions(session.Options{
			Config: aws.Config{
				Region: aws.String("us-east-1"),
			},
		}))

		/* Act */
		got := getPayModelTableCreds(sess)

		/* Assert */
		// Credentials should not be nil
		if got.Credentials == nil {
			t.Errorf("\nassertion error while testing `GetPayModelTableCredsWithArn` when %s : \ngot.Credentials should not be nil.",
				testcase.name)
		}
	}
}

func Test_PayModelFromConfig(t *testing.T) {
	defer SetupAndTeardownTest()()

	configWithoutPayModels := &FullHatcheryConfig{
		Config: HatcheryConfig{
			PayModelsDynamodbTable: "random_non_empty_string",
		},
	}

	testId := "payModelTest"
	defaultPayModelForTest := &PayModel{
		Name:  "Trial Workspace",
		User:  testId,
		Local: true,
	}
	MockPayModelMap := make(map[string]PayModel)
	MockPayModelMap[testId] = *defaultPayModelForTest
	configWithPayModel := &FullHatcheryConfig{
		Config: HatcheryConfig{
			PayModelsDynamodbTable: "random_non_empty_string",
		},
		PayModelMap: MockPayModelMap,
	}

	testCases := []struct {
		name                 string
		userName             string
		want                 *PayModel
		expectedErrorMessage string
		mockConfig           *FullHatcheryConfig
	}{
		{
			name:                 "NoPayModelsInConfig",
			userName:             testId,
			want:                 nil,
			expectedErrorMessage: "no paymodels found",
			mockConfig:           configWithoutPayModels,
		},
		{
			name:                 "UserHasPayModel",
			userName:             testId,
			want:                 defaultPayModelForTest,
			expectedErrorMessage: "",
			mockConfig:           configWithPayModel,
		},
	}

	for _, testcase := range testCases {
		t.Logf("Testing payModelFromConfig when %s", testcase.name)

		/* Setup */
		Config = testcase.mockConfig

		/* Act */
		got, err := payModelFromConfig(testcase.userName)

		/* Assert */
		var expectedError error
		if testcase.expectedErrorMessage != "" {
			expectedError = errors.New(testcase.expectedErrorMessage)
		} else {
			expectedError = nil
		}

		if (nil == expectedError) && (err != nil) {
			t.Errorf("\nError should be nil: \nGot:\n\t %+v", err)
		} else if err != nil && expectedError != nil && err.Error() != expectedError.Error() {
			t.Errorf("\nUnexpected error: \nWant:\n\t %+v,\nGot:\n\t %+v",
				testcase.expectedErrorMessage, err)
		}

		if !reflect.DeepEqual(got, testcase.want) {
			t.Errorf("\nassertion error while testing `payModelsFromConfig` when %s : \nWant:\n\t %+v,\nGot:\n\t %+v",
				testcase.name, testcase.want, got)
		}
	}
}
