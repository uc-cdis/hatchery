package hatchery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/smithy-go"
)

func (creds *CREDS) createTargetGroup(userName string, vpcId string, svc *elasticloadbalancingv2.Client) (*elasticloadbalancingv2.CreateTargetGroupOutput, error) {
	tgName := truncateString(strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-")+userToResourceName(userName, "service")+"tg", 32)
	input := &elasticloadbalancingv2.CreateTargetGroupInput{
		Name:            aws.String(tgName),
		Port:            aws.Int32(80),
		Protocol:        types.ProtocolEnumHttp,
		VpcId:           aws.String(vpcId),
		TargetType:      types.TargetTypeEnumIp,
		HealthCheckPath: aws.String("/lw-workspace/proxy/"),
		Matcher: &types.Matcher{
			HttpCode: aws.String("200-499"),
		},
	}

	//TODO: Consider using parent request context for propagation.
	//In AWS GO SDK V2 context is now required.
	//Currently we do not propagate context canceling to downstream requests
	//To preserve current behavior, local context is created.
	ctx = context.TODO()

	result, err := svc.CreateTargetGroup(ctx, input)

	//TODO: Consider typecasting error to specific type for more specific error handling
	//i.e. types.DuplicateTargetGroupNameException
	//Previous code only print out error type string, so there is no need for type casting
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			fmt.Println(apiErr.ErrorCode(), apiErr.ErrorMessage())
		} else {
			fmt.Println(err.Error())
		}
		return nil, err
	}

	return result, nil

}

func (creds *CREDS) setTargetGroupAttributes(svc *elasticloadbalancingv2.Client, targetGroupArn string) (*elasticloadbalancingv2.ModifyTargetGroupAttributesOutput, error) {
	modifyTargetGroupAttributesInput := &elasticloadbalancingv2.ModifyTargetGroupAttributesInput{
		TargetGroupArn: aws.String(targetGroupArn),
		Attributes: []types.TargetGroupAttribute{
			{
				Key:   aws.String("deregistration_delay.timeout_seconds"),
				Value: aws.String("0"),
			},
		},
	}

	ctx = context.TODO()

	modifyTargetGroup, err := svc.ModifyTargetGroupAttributes(ctx, modifyTargetGroupAttributesInput)

	if err != nil {
		return nil, err
	}
	return modifyTargetGroup, nil
}

func (creds *CREDS) createListener(svc *elasticloadbalancingv2.Client, loadBalancer string, targetGroup string) (*elasticloadbalancingv2.CreateListenerOutput, error) {
	input := &elasticloadbalancingv2.CreateListenerInput{
		DefaultActions: []types.Action{
			{
				TargetGroupArn: aws.String(targetGroup),
				Type:           types.ActionTypeEnumForward,
			},
		},
		LoadBalancerArn: aws.String(loadBalancer),
		Port:            aws.Int32(80),
		Protocol:        types.ProtocolEnumHttp,
	}

	ctx = context.TODO()

	result, err := svc.CreateListener(ctx, input)

	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			fmt.Println(apiErr.ErrorCode(), apiErr.ErrorMessage())
		} else {
			fmt.Println(err.Error())
		}
		return result, nil
	}

	return result, nil
}

func (creds *CREDS) CreateLoadBalancer(userName string) (*elasticloadbalancingv2.CreateLoadBalancerOutput, *string, *elasticloadbalancingv2.CreateListenerOutput, error) {

	//Client with Config replaces session in aws go sdk v1
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(aws.NewCredentialsCache(creds.creds)),
	)
	svc := elasticloadbalancingv2.NewFromConfig(cfg)

	networkInfo, err := creds.describeWorkspaceNetwork(userName)
	if err != nil {
		return nil, nil, nil, err
	}

	albName := truncateString(strings.ReplaceAll(userToResourceName(userName, "service")+os.Getenv("GEN3_ENDPOINT"), ".", "-")+"alb", 32)

	input := &elasticloadbalancingv2.CreateLoadBalancerInput{
		Name:   aws.String(albName),
		Scheme: types.LoadBalancerSchemeEnumInternal, // Corrected package destination
		SecurityGroups: []string{
			networkInfo.securityGroups.SecurityGroups[0].GroupId,
		},
		Subnets: []string{
			networkInfo.subnets.Subnets[0].SubnetId,
			networkInfo.subnets.Subnets[1].SubnetId,
		},
	}

	ctx = context.TODO()

	loadBalancer, err := svc.CreateLoadBalancer(ctx, input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			fmt.Println(apiErr.ErrorCode(), apiErr.ErrorMessage())
		} else {
			fmt.Println(err.Error())
		}
		return nil, nil, nil, err
	}

	targetGroup, err := creds.createTargetGroup(ctx, userName, *networkInfo.vpc.Vpcs[0].VpcId, svc)
	if err != nil {
		return nil, nil, nil, err
	}
	_, err = creds.setTargetGroupAttributes(ctx, svc, *targetGroup.TargetGroups[0].TargetGroupArn)
	if err != nil {
		return nil, nil, nil, err
	}
	listener, err := creds.createListener(ctx, svc, *loadBalancer.LoadBalancers[0].LoadBalancerArn, *targetGroup.TargetGroups[0].TargetGroupArn)
	if err != nil {
		return nil, nil, nil, err
	}
	return loadBalancer, targetGroup.TargetGroups[0].TargetGroupArn, listener, nil
}

func (creds *CREDS) terminateLoadBalancerTargetGroup(userName string) error {
	//Client with Config replaces session in aws go sdk v1
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(aws.NewCredentialsCache(creds.creds)),
	)
	svc := elasticloadbalancingv2.NewFromConfig(cfg)
	tgName := truncateString(strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-")+userToResourceName(userName, "service")+"tg", 32)
	Config.Logger.Printf("Deleting target group: %s", tgName)
	tgArn, err := svc.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{
		Names: []string{tgName},
	})
	if err != nil {
		var targetGroupNotFoundException *types.TargetGroupNotFoundException
		if errors.As(err, &targetGroupNotFoundException) {
			/ Target group not found, nothing to do
			return nil
		} else {
			Config.Logger.Printf("Error describing target group: %s", err.Error())
		}
		return err
	}

	input := &elasticloadbalancingv2.DeleteTargetGroupInput{
		TargetGroupArn: tgArn.TargetGroups[0].TargetGroupArn,
	}

	ctx = context.TODO()

	_, err = svc.DeleteTargetGroup(ctx, input)
	if err != nil {
		var resourceInUseException *types.ResourceInUseException
		if errors.As(err, &resourceInUseException) {
			// Target group in use, nothing to do
			return nil
		} else {
			Config.Logger.Printf("Error deleting target group: %s", err.Error())
		}
		return err
	}

	return nil
}

func (creds *CREDS) terminateLoadBalancer(ctx context.Context, userName string) error {
	//Client with Config replaces session in aws go sdk v1
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(aws.NewCredentialsCache(creds.creds)),
	)
	svc := elasticloadbalancingv2.NewFromConfig(cfg)

	albName := truncateString(strings.ReplaceAll(userToResourceName(userName, "service")+os.Getenv("GEN3_ENDPOINT"), ".", "-")+"alb", 32)

	getInput := &elasticloadbalancingv2.DescribeLoadBalancersInput{
		Names: []string{albName},
	}

	ctx = context.TODO()

	result, err := svc.DescribeLoadBalancers(ctx, getInput)
	if err != nil {
		var loadBalancerNotFoundException *types.LoadBalancerNotFoundException
		if errors.As(err, &loadBalancerNotFoundException) {
			// Load balancer doesn't exist, we are happy! :)
			return nil
		}
		else {
			return err
		}
	}

	if len(result.LoadBalancers) == 1 {
		delInput := &elasticloadbalancingv2.DeleteLoadBalancerInput{
			LoadBalancerArn: result.LoadBalancers[0].LoadBalancerArn,
		}
		_, err := svc.DeleteLoadBalancer(ctx, delInput)
		if err != nil {
			var loadBalancerNotFoundException *types.LoadBalancerNotFoundException
			if errors.As(err, &loadBalancerNotFoundException) {
				fmt.Printf("%T: %s\n", loadBalancerNotFoundException, loadBalancerNotFoundException.Error())
			}
			return err
		}
	}

	return nil
}
