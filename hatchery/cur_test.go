package hatchery

import (
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/costexplorer"
	"github.com/aws/aws-sdk-go/service/costexplorer/costexploreriface"
)

type MockCostOutput struct {
	output costexplorer.GetCostAndUsageOutput
}

type CostExplorerMockClient struct {
	costexploreriface.CostExplorerAPI
	mockOutput *MockCostOutput
}

func (m *CostExplorerMockClient) GetCostAndUsage(input *costexplorer.GetCostAndUsageInput) (*costexplorer.GetCostAndUsageOutput, error) {
	return &m.mockOutput.output, nil
}

func Test_GetCostUsageReport(t *testing.T) {
	defer SetupAndTeardownTest()()

	testCases := []struct {
		name           string
		want           *costUsage
		mockCostOutput *MockCostOutput
	}{
		{
			name: "UserHasCosts",
			want: &costUsage{Username: "test_user", TotalCost: 100},
			mockCostOutput: &MockCostOutput{
				output: costexplorer.GetCostAndUsageOutput{
					ResultsByTime: []*costexplorer.ResultByTime{
						{
							TimePeriod: &costexplorer.DateInterval{
								// 1 year ago is max
								Start: aws.String(time.Now().AddDate(-1, 0, 0).Format("2006-01-02")),
								// Today
								End: aws.String(time.Now().Format("2006-01-02")),
							},
							Total: map[string]*costexplorer.MetricValue{
								"UnblendedCost": {
									Amount: aws.String("100"),
									Unit:   aws.String("USD"),
								},
							},
						},
					},
				},
			},
		},
		{
			name:           "NoUserCosts",
			want:           &costUsage{Username: "test_user", TotalCost: 0},
			mockCostOutput: &MockCostOutput{},
		},
	}

	// mock the cost explorer interface
	costexplorerclient := initializeCostExplorerClient()

	for _, testcase := range testCases {
		t.Logf("Testing GetCostUsageReport when %s", testcase.name)

		costexplorerclient.CostExporer = &CostExplorerMockClient{
			CostExplorerAPI: nil,
			mockOutput:      testcase.mockCostOutput,
		}

		/* Act */
		got, err := getCostUsageReport(costexplorerclient, "test_user", "Direct Pay")
		if nil != err {
			t.Errorf("failed to get cost usage report, got: %v", err)
			return
		}

		/* Assert */
		if reflect.TypeOf(got) != reflect.TypeOf(testcase.want) {
			t.Errorf("Return value is not correct type:\ngot: '%v'\nwant: '%v'",
				reflect.TypeOf(got), reflect.TypeOf(testcase.want))
		}
		if !reflect.DeepEqual(got, testcase.want) {
			t.Errorf("\nassertion error while testing `getCostUsageReport`: \nWant:%+v\nGot:%+v", testcase.want, got)
		}

	}
}
