package hatchery

import (
	"encoding/json"
	"reflect"
	"testing"
)

func Test_GetCurrentPayModel_Returns_DefaultPayModel_When_NoDB(t *testing.T) {

	mockConfig := &FullHatcheryConfig{
		Config: HatcheryConfig{
			PayModelsDynamodbTable: "",
		},
	}
	var mockDefaultPaymodel *PayModel
	_ = json.Unmarshal([]byte(`{
		"workspace_type": "Trial Workspace",
		"local": true
	}`), &mockDefaultPaymodel)

	/***Patching***/
	Config = mockConfig

	// Patching the behavior of getDefaultPayModel with mock implementation
	getDefaultPayModel = func() (*PayModel, error) {
		return mockDefaultPaymodel, nil
	}

	/** Testing **/
	payModel, err := getCurrentPayModel("testUser")
	if nil != err {
		t.Errorf("failed to load current pay model, got: %v", err)
		return
	}

	if !reflect.DeepEqual(payModel, mockDefaultPaymodel) {
		t.Errorf("assertion error: \nexpected %+v,\ngot: %+v", mockDefaultPaymodel, payModel)
		return
	}

}

func Test_GetCurrentPayModel_Returns_CurrentPayModel_When_CurrentPayModelExists(t *testing.T) {
	mockConfig := &FullHatcheryConfig{
		Config: HatcheryConfig{
			PayModelsDynamodbTable: "random_non_empty_string",
		},
	}
	mockPayModelsFromDBWithCurrent := []PayModel{
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
	}

	Config = mockConfig
	payModelsFromDatabase = func(userName string, current bool) (payModels *[]PayModel, err error) {
		if current {
			return &[]PayModel{mockPayModelsFromDBWithCurrent[0]}, nil
		}
		return &mockPayModelsFromDBWithCurrent, nil
	}

	/** Testing **/
	payModel, err := getCurrentPayModel("testUser")
	if nil != err {
		t.Errorf("failed to load current pay model, got: %v", err)
		return
	}

	if !reflect.DeepEqual(payModel, &mockPayModelsFromDBWithCurrent[0]) {
		t.Errorf("assertion error: \nexpected %+v,\ngot: %+v", mockPayModelsFromDBWithCurrent[0], payModel)
		return
	}
}

func Test_GetCurrentPayModel_Returns_Nil_When_ActiveButNotCurrentPaymodelExists(t *testing.T) {
	mockConfig := &FullHatcheryConfig{
		Config: HatcheryConfig{
			PayModelsDynamodbTable: "random_non_empty_string",
		},
	}

	mockPayModelsFromDBNoCurrentPayModel := []PayModel{
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
	}

	Config = mockConfig
	payModelsFromDatabase = func(userName string, current bool) (payModels *[]PayModel, err error) {
		if current {
			return &[]PayModel{}, nil
		}
		return &mockPayModelsFromDBNoCurrentPayModel, nil
	}

	/** Testing **/
	payModel, err := getCurrentPayModel("testUser")
	if nil != err {
		t.Errorf("failed to load current pay model, got: %v", err)
		return
	}

	if payModel != nil {
		t.Errorf("assertion error: \nexpected %+v,\ngot: %+v", nil, payModel)
		return
	}
}

func Test_GetCurrentPayModel_Returns_DefaultPayModel_When_NeitherCurrentNorActivePaymodelExists(t *testing.T) {
	mockConfig := &FullHatcheryConfig{
		Config: HatcheryConfig{
			PayModelsDynamodbTable: "random_non_empty_string",
		},
	}

	var mockDefaultPaymodel *PayModel
	_ = json.Unmarshal([]byte(`{
		"workspace_type": "Trial Workspace",
		"local": true
	}`), &mockDefaultPaymodel)

	Config = mockConfig
	getDefaultPayModel = func() (*PayModel, error) {
		return mockDefaultPaymodel, nil
	}
	payModelsFromDatabase = func(userName string, current bool) (payModels *[]PayModel, err error) {
		// When there are no active or above limit pay models this function returns empty array
		// for both when current is true or false
		return &[]PayModel{}, nil
	}

	/** Testing **/
	payModel, err := getCurrentPayModel("testUser")
	if nil != err {
		t.Errorf("failed to load current pay model, got: %v", err)
		return
	}

	if !reflect.DeepEqual(payModel, mockDefaultPaymodel) {
		t.Errorf("assertion error: \nexpected %+v,\ngot: %+v", mockDefaultPaymodel, payModel)
		return
	}
}
