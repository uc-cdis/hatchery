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

type DynamodbMockClient struct {
	dynamodbiface.DynamoDBAPI
	mockOutput *dynamodb.QueryOutput
}

func (m *DynamodbMockClient) Query(input *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
	return m.mockOutput, nil
}

func Test_GetActiveGen3UserLicenses(t *testing.T) {
	defer SetupAndTeardownTest()()

	targetEnvironment := "test.planx-pla.net"
	t.Setenv("GEN3_ENDPOINT", targetEnvironment)

	testCases := []struct {
		name            string
		want            *[]Gen3UserLicense
		mockQueryOutput *dynamodb.QueryOutput
	}{
		{
			name:            "NoActiveLicenses",
			want:            &[]Gen3UserLicense{},
			mockQueryOutput: &dynamodb.QueryOutput{},
		},
		{
			name: "SomeActiveLicenses",
			want: &[]Gen3UserLicense{
				{
					ItemId:      "1234-abcd",
					Environment: targetEnvironment,
					IsActive:    "True",
					LicenseId:   1,
				},
				{
					ItemId:      "5678-efgh",
					Environment: targetEnvironment,
					IsActive:    "True",
					LicenseId:   2,
				},
			},
			mockQueryOutput: &dynamodb.QueryOutput{
				Items: []map[string]*dynamodb.AttributeValue{
					{
						"ItemId":      {S: aws.String("1234-abcd")},
						"Environment": {S: aws.String(targetEnvironment)},
						"IsActive":    {S: aws.String("True")},
						"LicenseId":   {N: aws.String("1")},
					},
					{
						"ItemId":      {S: aws.String("5678-efgh")},
						"Environment": {S: aws.String(targetEnvironment)},
						"IsActive":    {S: aws.String("True")},
						"LicenseId":   {N: aws.String("2")},
					},
				},
			},
		},
	}

	// mock the db
	dbconfig := initializeDbConfig()

	for _, testcase := range testCases {
		t.Logf("Testing GetActiveGen3UserLicenses")

		dbconfig.DynamoDb = &DynamodbMockClient{
			DynamoDBAPI: nil,
			mockOutput:  testcase.mockQueryOutput,
		}

		/* Act */
		got, err := getActiveGen3UserLicenses(dbconfig)
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
			t.Errorf("\nassertion error while testing `getActiveGen3UserLicenses`: \nWant:%+v\nGot:%+v", testcase.want, got)
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

func Test_CreateGen3UserLicense(t *testing.T) {

	/* Setup */
	targetEnvironment := "test.planx-pla.net"
	t.Setenv("GEN3_ENDPOINT", targetEnvironment)
	itemId := "testItem"
	licenseId := 1

	dbconfig := initializeDbConfig()
	dbconfig.DynamoDb = &DynamodbMockClient{}

	t.Logf("Testing CreateGen3UserLicense")

	/* Act */
	newItem, err := createGen3UserLicense(dbconfig, itemId, licenseId)
	if nil != err {
		t.Errorf("failed to put item, got: %v", err)
	}

	/* Assert */
	if reflect.TypeOf(newItem) != reflect.TypeOf(Gen3UserLicense{}) {
		t.Errorf("Return value is not correct type:\ngot: '%v'\nwant: '%v'",
			reflect.TypeOf(newItem), reflect.TypeOf(Gen3UserLicense{}))
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

func Test_SetGen3UserLicenseInactive(t *testing.T) {
	defer SetupAndTeardownTest()()

	/* Setup */
	targetEnvironment := "test.planx-pla.net"
	t.Setenv("GEN3_ENDPOINT", targetEnvironment)
	itemId := "testItem"

	dbconfig := initializeDbConfig()
	dbconfig.DynamoDb = &DynamodbMockClient{}

	t.Logf("Testing SetGen3UserLicenseInactive when %s", "singleitemtoupdate")

	/* Act */
	updatedItem, err := setGen3UserLicenseInactive(dbconfig, itemId)
	if nil != err {
		t.Errorf("failed to put item, got: %v", err)
		return
	}

	/* Assert */
	if reflect.TypeOf(updatedItem) != reflect.TypeOf(Gen3UserLicense{}) {
		t.Errorf("Return value is not correct type:\ngot: '%v'\nwant: '%v'",
			reflect.TypeOf(updatedItem), reflect.TypeOf(Gen3UserLicense{}))
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
		name                       string
		maxLicenseIds              int
		want                       int
		mockActiveGen3UserLicenses *[]Gen3UserLicense
	}{
		{
			name:                       "Gen3UserLicensesIsEmpty",
			maxLicenseIds:              6,
			want:                       1,
			mockActiveGen3UserLicenses: &[]Gen3UserLicense{},
		},
		{
			name:          "OneLicenseUsed",
			maxLicenseIds: 6,
			want:          2,
			mockActiveGen3UserLicenses: &[]Gen3UserLicense{
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
			mockActiveGen3UserLicenses: &[]Gen3UserLicense{
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

		got := getNextLicenseId(testcase.mockActiveGen3UserLicenses, testcase.maxLicenseIds)

		/* Assert */
		if got != testcase.want {
			t.Errorf("\nassertion error while testing `getNextLicenseId`: \nWant:%+v\nGot:%+v", testcase.want, got)
		}
	}

}

func Test_GetLicenseFromKubernetes(t *testing.T) {
	defer SetupAndTeardownTest()()

	g3autoName := Config.Config.Gen3G3autoName
	g3autoKey := Config.Config.Gen3G3autoKey
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

		got, err := getLicenseFromKubernetes(fakeClientset)
		if err != nil {
			t.Logf("Error in reading license from kubernetes: %s", err)
		}
		/* Assert */
		if got != testcase.want {
			t.Errorf("\nassertion error while testing `getNextLicenseId`: \nWant:%+v\nGot:%+v", testcase.want, got)
		}
	}

}
