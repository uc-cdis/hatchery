package hatchery

import (
	"reflect"
	"testing"
)

func Test_GetCurrentPayModel(t *testing.T) {
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
