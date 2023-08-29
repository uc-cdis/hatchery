package hatchery

import (
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/costexplorer"
)

type costUsage struct {
	Username  string  `json:"username"`
	TotalCost float64 `json:"total-cost"`
}

func getCostUsageReport(username string, workflowname string) (*costUsage, error) {
	// query cost usage report
	// Create a Cost Explorer service client
	sess := session.Must(session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable,
	}))
	svc := costexplorer.New(sess)
	// Build the request with date range and filter
	// Return costs by tags
	req := &costexplorer.GetCostAndUsageInput{
		Metrics: []*string{
			aws.String("UnblendedCost"),
		},
		TimePeriod: &costexplorer.DateInterval{
			// 1 year ago is max
			Start: aws.String(time.Now().AddDate(-1, 0, 0).Format("2006-01-02")),
			// Today
			End: aws.String(time.Now().Format("2006-01-02")),
		},
		Filter: &costexplorer.Expression{
			Tags: &costexplorer.TagValues{
				Key: aws.String("gen3username"),
				Values: []*string{
					aws.String(userToResourceName(username, "user")),
				},
			},
		},
		Granularity: aws.String("MONTHLY"),
	}

	if workflowname != "" {
		req.Filter = &costexplorer.Expression{
			Tags: &costexplorer.TagValues{
				Key: aws.String("gen3username"),
				Values: []*string{
					aws.String(userToResourceName(username, "user")),
				},
			},
		}
	}

	// Call Cost Explorer API
	resp, err := svc.GetCostAndUsage(req)
	if err != nil {
		fmt.Println("Got error calling GetCostAndUsage:", err)
		return nil, err
	}
	var total float64
	for _, result := range resp.ResultsByTime {
		// Get amount
		totalAmount := result.Total["UnblendedCost"]
		amount, _ := strconv.ParseFloat(*totalAmount.Amount, 64)

		// Sum amounts
		total += amount
	}

	ret := costUsage{Username: username, TotalCost: total}

	return &ret, nil
}
