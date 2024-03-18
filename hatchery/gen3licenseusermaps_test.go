package hatchery

import (
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbiface"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

type MockOutputPages struct {
	first  dynamodb.QueryOutput
	second dynamodb.QueryOutput
}

type DynamodbMockClient struct {
	dynamodbiface.DynamoDBAPI
	mockOutput *MockOutputPages
}

func (m *DynamodbMockClient) Query(input *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
	if input.ExclusiveStartKey == nil {
		return &m.mockOutput.first, nil
	} else {
		return &m.mockOutput.second, nil
	}
}

func Test_GetActiveGen3LicenseUserMaps(t *testing.T) {
	defer SetupAndTeardownTest()()

	targetEnvironment := "test.planx-pla.net"
	t.Setenv("GEN3_ENDPOINT", targetEnvironment)

	firstMockItems := []map[string]*dynamodb.AttributeValue{
		{
			"ItemId":      {S: aws.String("1234-abcd")},
			"Environment": {S: aws.String(targetEnvironment)},
			"IsActive":    {S: aws.String("True")},
			"LicenseId":   {N: aws.String("1")},
		},
		{
			"ItemId":      {S: aws.String("1234-efgh")},
			"Environment": {S: aws.String(targetEnvironment)},
			"IsActive":    {S: aws.String("True")},
			"LicenseId":   {N: aws.String("2")},
		},
	}
	secondMockItems := []map[string]*dynamodb.AttributeValue{
		{
			"ItemId":      {S: aws.String("5678-abcd")},
			"Environment": {S: aws.String(targetEnvironment)},
			"IsActive":    {S: aws.String("True")},
			"LicenseId":   {N: aws.String("3")},
		},
		{
			"ItemId":      {S: aws.String("5678-efgh")},
			"Environment": {S: aws.String(targetEnvironment)},
			"IsActive":    {S: aws.String("True")},
			"LicenseId":   {N: aws.String("4")},
		},
	}

	testCases := []struct {
		name               string
		want               []Gen3LicenseUserMap
		mockLicenseEnabled bool
		mockQueryOutput    *MockOutputPages
	}{
		{
			name:               "LicenseNotEnabled",
			want:               []Gen3LicenseUserMap{},
			mockLicenseEnabled: false,
			mockQueryOutput:    &MockOutputPages{},
		},
		{
			name:               "NoActiveLicenses",
			want:               []Gen3LicenseUserMap{},
			mockLicenseEnabled: true,
			mockQueryOutput:    &MockOutputPages{},
		},
		{
			name: "SomeActiveLicenses",
			want: []Gen3LicenseUserMap{
				{
					ItemId:      "1234-abcd",
					Environment: targetEnvironment,
					IsActive:    "True",
					LicenseId:   1,
				},
				{
					ItemId:      "1234-efgh",
					Environment: targetEnvironment,
					IsActive:    "True",
					LicenseId:   2,
				},
			},
			mockLicenseEnabled: true,
			mockQueryOutput: &MockOutputPages{
				first: dynamodb.QueryOutput{
					Items: firstMockItems,
				},
			},
		},
		{
			name: "PaginatedActiveLicenses",
			want: []Gen3LicenseUserMap{
				{
					ItemId:      "1234-abcd",
					Environment: targetEnvironment,
					IsActive:    "True",
					LicenseId:   1,
				},
				{
					ItemId:      "1234-efgh",
					Environment: targetEnvironment,
					IsActive:    "True",
					LicenseId:   2,
				},
				{
					ItemId:      "5678-abcd",
					Environment: targetEnvironment,
					IsActive:    "True",
					LicenseId:   3,
				},
				{
					ItemId:      "5678-efgh",
					Environment: targetEnvironment,
					IsActive:    "True",
					LicenseId:   4,
				},
			},
			mockLicenseEnabled: true,
			mockQueryOutput: &MockOutputPages{
				first: dynamodb.QueryOutput{
					Items: firstMockItems,
					LastEvaluatedKey: map[string]*dynamodb.AttributeValue{
						"ItemId": {S: aws.String("1234-efgh")},
					},
				},
				second: dynamodb.QueryOutput{
					Items: secondMockItems,
				},
			},
		},
	}

	// mock the db
	dbconfig := initializeDbConfig()

	licenseInfo := LicenseInfo{
		LicenseType:     "some-license",
		MaxLicenseIds:   6,
		G3autoName:      "stata-workspace-gen3-license-g3auto",
		G3autoKey:       "stata_license.txt",
		FilePath:        "stata.lic",
		WorkspaceFlavor: "gen3-licensed",
	}
	mockContainer := Container{
		Name:    "container-name",
		License: licenseInfo,
	}

	// getActiveGen3LicenseUserMaps
	for _, testcase := range testCases {
		t.Logf("Testing GetActiveGen3LicenseUserMaps case: %s", testcase.name)

		dbconfig.DynamoDb = &DynamodbMockClient{
			DynamoDBAPI: nil,
			mockOutput:  testcase.mockQueryOutput,
		}

		mockContainer.License.Enabled = testcase.mockLicenseEnabled

		/* Act */
		got, err := getActiveGen3LicenseUserMaps(dbconfig, mockContainer)
		if nil != err {
			t.Errorf("failed to query table, got: %v", err)
			return
		}

		/* Assert */
		if reflect.TypeOf(got) != reflect.TypeOf(testcase.want) {
			t.Errorf("Return value is not correct type:\ngot: '%v'\nwant: '%v'",
				reflect.TypeOf(got), reflect.TypeOf(testcase.want))
		}
		if !reflect.DeepEqual(got, testcase.want) {
			t.Errorf("\nassertion error while testing `getActiveGen3LicenseUserMaps`: \nWant:%+v\nGot:%+v", testcase.want, got)
		}
	}

	// getLicenseUserMapsForUser
	for _, testcase := range testCases {
		t.Logf("Testing getLicenseUserMapsForUser case: %s", testcase.name)

		dbconfig.DynamoDb = &DynamodbMockClient{
			DynamoDBAPI: nil,
			mockOutput:  testcase.mockQueryOutput,
		}

		/* Act */
		got, err := getLicenseUserMapsForUser(dbconfig, "some-user")
		if nil != err {
			t.Errorf("failed to query table, got: %v", err)
			return
		}

		/* Assert */
		if reflect.TypeOf(got) != reflect.TypeOf(testcase.want) {
			t.Errorf("Return value is not correct type:\ngot: '%v'\nwant: '%v'",
				reflect.TypeOf(got), reflect.TypeOf(testcase.want))
		}
		if !reflect.DeepEqual(got, testcase.want) {
			t.Errorf("\nassertion error while testing `getLicenseUserMapsForUser`: \nWant:%+v\nGot:%+v", testcase.want, got)
		}
	}
}

func (m *DynamodbMockClient) PutItem(input *dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{
		Attributes: map[string]*dynamodb.AttributeValue{
			"IsActive": {S: aws.String("True")},
		},
	}, nil
}

func Test_CreateGen3LicenseUserMap(t *testing.T) {

	/* Setup */
	targetEnvironment := "test.planx-pla.net"
	t.Setenv("GEN3_ENDPOINT", targetEnvironment)
	itemId := "testItem"
	licenseId := 1

	dbconfig := initializeDbConfig()
	dbconfig.DynamoDb = &DynamodbMockClient{}

	licenseInfo := LicenseInfo{
		Enabled:       true,
		LicenseType:   "some-license",
		MaxLicenseIds: 6,
	}
	mockContainer := Container{
		Name:    "container-name",
		License: licenseInfo,
	}

	t.Logf("Testing CreateGen3LicenseUserMap")

	/* Act */
	newItem, err := createGen3LicenseUserMap(dbconfig, itemId, licenseId, mockContainer)
	if nil != err {
		t.Errorf("failed to put item, got: %v", err)
	}

	/* Assert */
	if reflect.TypeOf(newItem) != reflect.TypeOf(Gen3LicenseUserMap{}) {
		t.Errorf("Return value is not correct type:\ngot: '%v'\nwant: '%v'",
			reflect.TypeOf(newItem), reflect.TypeOf(Gen3LicenseUserMap{}))
	}
	got := newItem.IsActive
	want := "True"
	if got != want {
		t.Errorf("Update did not set isActive flag:\ngot: '%v'\nwant: '%v'",
			got, want)
	}

}

func (m *DynamodbMockClient) UpdateItem(input *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{
		Attributes: map[string]*dynamodb.AttributeValue{
			"isActive": {S: aws.String("False")},
		},
	}, nil
}

func Test_SetGen3LicenseUserInactive(t *testing.T) {
	defer SetupAndTeardownTest()()

	/* Setup */
	targetEnvironment := "test.planx-pla.net"
	t.Setenv("GEN3_ENDPOINT", targetEnvironment)
	itemId := "testItem"

	dbconfig := initializeDbConfig()
	dbconfig.DynamoDb = &DynamodbMockClient{}

	t.Logf("Testing SetGen3LicenseUserInactive when %s", "singleitemtoupdate")

	/* Act */
	updatedItem, err := setGen3LicenseUserInactive(dbconfig, itemId)
	if nil != err {
		t.Errorf("failed to put item, got: %v", err)
		return
	}

	/* Assert */
	if reflect.TypeOf(updatedItem) != reflect.TypeOf(Gen3LicenseUserMap{}) {
		t.Errorf("Return value is not correct type:\ngot: '%v'\nwant: '%v'",
			reflect.TypeOf(updatedItem), reflect.TypeOf(Gen3LicenseUserMap{}))
	}
	got := updatedItem.IsActive
	want := "False"
	if got != want {
		t.Errorf("Update did not set isActive flag:\ngot: '%v'\nwant: '%v'",
			got, want)
	}

}

func Test_GetNextLicenseId(t *testing.T) {

	testCases := []struct {
		name                          string
		maxLicenseIds                 int
		want                          int
		mockActiveGen3LicenseUserMaps []Gen3LicenseUserMap
	}{
		{
			name:                          "Gen3UserLicensesIsEmpty",
			maxLicenseIds:                 6,
			want:                          1,
			mockActiveGen3LicenseUserMaps: []Gen3LicenseUserMap{},
		},
		{
			name:          "OneLicenseUsed",
			maxLicenseIds: 6,
			want:          2,
			mockActiveGen3LicenseUserMaps: []Gen3LicenseUserMap{
				{
					IsActive:  "True",
					LicenseId: 1,
				},
			},
		},
		{
			name:          "MaxLicensesActive",
			maxLicenseIds: 2,
			want:          0,
			mockActiveGen3LicenseUserMaps: []Gen3LicenseUserMap{
				{
					IsActive:  "True",
					LicenseId: 1,
				},
				{
					IsActive:  "True",
					LicenseId: 2,
				},
			},
		},
	}

	for _, testcase := range testCases {
		t.Logf("Testing getNextLicenseId when %s", testcase.name)

		got := getNextLicenseId(testcase.mockActiveGen3LicenseUserMaps, testcase.maxLicenseIds)

		/* Assert */
		if got != testcase.want {
			t.Errorf("\nassertion error while testing `getNextLicenseId`: \nWant:%+v\nGot:%+v", testcase.want, got)
		}
	}

}

func Test_GetLicenseFromKubernetes(t *testing.T) {
	defer SetupAndTeardownTest()()

	g3autoName := "g3auto-name"
	g3autoKey := "g3auto-key"
	kubeNamespace := "default"
	testCases := []struct {
		name    string
		want    string
		secrets []runtime.Object
	}{
		{
			name: "secretPresent",
			want: "my_super_secret_123",
			secrets: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      g3autoName,
						Namespace: kubeNamespace,
					},
					Data: map[string][]byte{
						g3autoKey: []byte("my_super_secret_123"),
					},
				},
			},
		},
		{
			name: "secretNotPresent",
			want: "",
			secrets: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "NOT-" + g3autoName,
						Namespace: kubeNamespace,
					},
					Data: map[string][]byte{
						g3autoKey: []byte("my_other_super_secret_123"),
					},
				},
			},
		},
		{
			name: "secretPresentKeyNotPresent",
			want: "",
			secrets: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      g3autoName,
						Namespace: kubeNamespace,
					},
					Data: map[string][]byte{
						"NOT-" + g3autoKey: []byte("my_other_super_secret_123"),
					},
				},
			},
		},
	}

	for _, testcase := range testCases {
		t.Logf("Testing getLicenseFromKubernetes when %s", testcase.name)
		fakeClientset := fake.NewSimpleClientset(testcase.secrets...)

		got, err := getLicenseFromKubernetes(fakeClientset, g3autoName, g3autoKey)
		if err != nil {
			t.Logf("Error in reading license from kubernetes: %s", err)
		}
		/* Assert */
		if got != testcase.want {
			t.Errorf("\nassertion error while testing `getNextLicenseId`: \nWant:%+v\nGot:%+v", testcase.want, got)
		}
	}

}

func Test_ValidateContainerLicenseInfo(t *testing.T) {

	testCases := []struct {
		name        string
		licenseInfo LicenseInfo
		want        bool
	}{
		{
			name: "ValidLicenseInfo",
			want: true,
			licenseInfo: LicenseInfo{
				Enabled:         true,
				LicenseType:     "test-license-type",
				MaxLicenseIds:   3,
				G3autoName:      "test-g3auto-name",
				G3autoKey:       "test0g3auto-key",
				FilePath:        "test-file-path",
				WorkspaceFlavor: "test-workspace-flavor",
			},
		},
		{
			name: "LicenseNotEnabled",
			want: false,
			licenseInfo: LicenseInfo{
				Enabled:         false,
				LicenseType:     "test-license-type",
				MaxLicenseIds:   3,
				G3autoName:      "test-g3auto-name",
				G3autoKey:       "test0g3auto-key",
				FilePath:        "test-file-path",
				WorkspaceFlavor: "test-workspace-flavor",
			},
		},
		{
			name: "MissingLicenseType",
			want: false,
			licenseInfo: LicenseInfo{
				Enabled:         true,
				MaxLicenseIds:   3,
				G3autoName:      "test-g3auto-name",
				G3autoKey:       "test0g3auto-key",
				FilePath:        "test-file-path",
				WorkspaceFlavor: "test-workspace-flavor",
			},
		},
		{
			name: "ZeroMaxIds",
			want: false,
			licenseInfo: LicenseInfo{
				Enabled:         true,
				LicenseType:     "test-license-type",
				MaxLicenseIds:   0,
				G3autoName:      "test-g3auto-name",
				G3autoKey:       "test0g3auto-key",
				FilePath:        "test-file-path",
				WorkspaceFlavor: "test-workspace-flavor",
			},
		},
		{
			name: "MissingG3AutoName",
			want: false,
			licenseInfo: LicenseInfo{
				Enabled:         true,
				LicenseType:     "test-license-type",
				MaxLicenseIds:   3,
				G3autoKey:       "test0g3auto-key",
				FilePath:        "test-file-path",
				WorkspaceFlavor: "test-workspace-flavor",
			},
		},
	}

	for _, testcase := range testCases {
		t.Logf("Testing validateContainerLicenseInfo when %s", testcase.name)

		got := validateContainerLicenseInfo("container-name", testcase.licenseInfo)

		/* Assert */
		if got != testcase.want {
			t.Errorf("\nassertion error while testing `validateContainerLicenseInfo`: \nWant:%+v\nGot:%+v", testcase.want, got)
		}
	}

}
