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
)

func (creds *CREDS) createTargetGroup(ctx context.Context, userName string, vpcId string, svc *elasticloadbalancingv2.Client) (*elasticloadbalancingv2.CreateTargetGroupOutput, error) {
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

	result, err := svc.CreateTargetGroup(ctx, input)

	if err != nil {
		var duplicateTargetGroupErr *types.DuplicateTargetGroupNameException
		var tooManyTargetGroupsException *types.TooManyTargetGroupsException
		var invalidConfigurationRequestException *types.InvalidConfigurationRequestException
		var tooManyTagsException *types.TooManyTagsException

		if errors.As(err, &duplicateTargetGroupErr) {
			fmt.Println("%T: %s\n", duplicateTargetGroupErr, duplicateTargetGroupErr.Error())
		} else if errors.As(err, &tooManyTargetGroupsException) {
			fmt.Println("%T: %s\n", tooManyTargetGroupsException, tooManyTargetGroupsException.Error())
		} else if errors.As(err, &invalidConfigurationRequestException) {
			fmt.Println("%T: %s\n", invalidConfigurationRequestException, invalidConfigurationRequestException.Error())
		} else if errors.As(err, &tooManyTagsException) {
			fmt.Println("%T: %s\n", tooManyTagsException, tooManyTagsException.Error())
		} else {
			fmt.Println(err.Error())
			return nil, err
		}

		return nil, err
	}

	return result, nil

}

func (creds *CREDS) setTargetGroupAttributes(ctx context.Context, svc *elasticloadbalancingv2.Client, targetGroupArn string) (*elasticloadbalancingv2.ModifyTargetGroupAttributesOutput, error) {
	modifyTargetGroupAttributesInput := &elasticloadbalancingv2.ModifyTargetGroupAttributesInput{
		TargetGroupArn: aws.String(targetGroupArn),
		Attributes: []types.TargetGroupAttribute{
			{
				Key:   aws.String("deregistration_delay.timeout_seconds"),
				Value: aws.String("0"),
			},
		},
	}
	modifyTargetGroup, err := svc.ModifyTargetGroupAttributes(ctx, modifyTargetGroupAttributesInput)

	if err != nil {
		return nil, err
	}
	return modifyTargetGroup, nil
}

func (creds *CREDS) createListener(ctx context.Context, svc *elasticloadbalancingv2.Client, loadBalancer string, targetGroup string) (*elasticloadbalancingv2.CreateListenerOutput, error) {
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

	result, err := svc.CreateListener(ctx, input)
	if err != nil {
		//Type cast enables code to read extra properties sent back by AWS inside the error struct itself (i.e. duplicateListenerErr.StorageLimit)
		var duplicateListenerException *types.DuplicateListenerException
		var tooManyListenersException *types.TooManyListenersException
		var tooManyCertificatesException *types.TooManyCertificatesException
		var loadBalancerNotFoundException *types.LoadBalancerNotFoundException
		var targetGroupNotFoundException *types.TargetGroupNotFoundException
		var targetGroupAssociationLimitException *types.TargetGroupAssociationLimitException
		var invalidConfigurationRequestException *types.InvalidConfigurationRequestException
		var incompatibleProtocolsException *types.IncompatibleProtocolsException
		var SSLPolicyNotFoundException *types.SSLPolicyNotFoundException
		var certificateNotFoundException *types.CertificateNotFoundException
		var unsupportedProtocolException *types.UnsupportedProtocolException
		var tooManyRegistrationsForTargetIdException *types.TooManyRegistrationsForTargetIdException
		var tooManyTargetsException *types.TooManyTargetsException
		var tooManyActionsException *types.TooManyActionsException
		var invalidLoadBalancerActionException *types.InvalidLoadBalancerActionException
		var tooManyUniqueTargetGroupsPerLoadBalancerException *types.TooManyUniqueTargetGroupsPerLoadBalancerException
		var ALPNPolicyNotSupportedException *types.ALPNPolicyNotSupportedException
		var tooManyTagsException *types.TooManyTagsException

		if errors.As(err, &duplicateListenerException) {
			fmt.Println("%T: %s\n", duplicateListenerException, duplicateListenerException.Error())
		} else if errors.As(err, &tooManyListenersException) {
			fmt.Println("%T: %s\n", tooManyListenersException, tooManyListenersException.Error())
		} else if errors.As(err, &tooManyCertificatesException) {
			fmt.Println("%T: %s\n", tooManyCertificatesException, tooManyCertificatesException.Error())
		} else if errors.As(err, &loadBalancerNotFoundException) {
			fmt.Println("%T: %s\n", loadBalancerNotFoundException, loadBalancerNotFoundException.Error())
		} else if errors.As(err, &targetGroupNotFoundException) {
			fmt.Println("%T: %s\n", targetGroupNotFoundException, targetGroupNotFoundException.Error())
		} else if errors.As(err, &targetGroupAssociationLimitException) {
			fmt.Println("%T: %s\n", targetGroupAssociationLimitException, targetGroupAssociationLimitException.Error())
		} else if errors.As(err, &invalidConfigurationRequestException) {
			fmt.Println("%T: %s\n", invalidConfigurationRequestException, invalidConfigurationRequestException.Error())
		} else if errors.As(err, &incompatibleProtocolsException) {
			fmt.Println("%T: %s\n", incompatibleProtocolsException, incompatibleProtocolsException.Error())
		} else if errors.As(err, &SSLPolicyNotFoundException) {
			fmt.Println("%T: %s\n", SSLPolicyNotFoundException, SSLPolicyNotFoundException.Error())
		} else if errors.As(err, &certificateNotFoundException) {
			fmt.Println("%T: %s\n", certificateNotFoundException, certificateNotFoundException.Error())
		} else if errors.As(err, &unsupportedProtocolException) {
			fmt.Println("%T: %s\n", unsupportedProtocolException, unsupportedProtocolException.Error())
		} else if errors.As(err, &tooManyRegistrationsForTargetIdException) {
			fmt.Println("%T: %s\n", tooManyRegistrationsForTargetIdException, tooManyRegistrationsForTargetIdException.Error())
		} else if errors.As(err, &tooManyTargetsException) {
			fmt.Println("%T: %s\n", tooManyTargetsException, tooManyTargetsException.Error())
		} else if errors.As(err, &tooManyActionsException) {
			fmt.Println("%T: %s\n", tooManyActionsException, tooManyActionsException.Error())
		} else if errors.As(err, &invalidLoadBalancerActionException) {
			fmt.Println("%T: %s\n", invalidLoadBalancerActionException, invalidLoadBalancerActionException.Error())
		} else if errors.As(err, &tooManyUniqueTargetGroupsPerLoadBalancerException) {
			fmt.Println("%T: %s\n", tooManyUniqueTargetGroupsPerLoadBalancerException, tooManyUniqueTargetGroupsPerLoadBalancerException.Error())
		} else if errors.As(err, &ALPNPolicyNotSupportedException) {
			fmt.Println("%T: %s\n", ALPNPolicyNotSupportedException, ALPNPolicyNotSupportedException.Error())
		} else if errors.As(err, &tooManyTagsException) {
			fmt.Println("%T: %s\n", tooManyTagsException, tooManyTagsException.Error())
		} else {
			fmt.Println(err.Error())
		}

		return nil, err
	}

	return result, nil
}

func (creds *CREDS) CreateLoadBalancer(ctx context.Context, userName string) (*elasticloadbalancingv2.CreateLoadBalancerOutput, *string, *elasticloadbalancingv2.CreateListenerOutput, error) {

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

	loadBalancer, err := svc.CreateLoadBalancer(ctx, input)
	if err != nil {
		var duplicateLoadBalancerName *types.DuplicateLoadBalancerNameException
		var tooManyLoadBalancers *types.TooManyLoadBalancersException
		var invalidConfigurationRequest *types.InvalidConfigurationRequestException
		var subnetNotFound *types.SubnetNotFoundException
		var invalidSubnet *types.InvalidSubnetException
		var invalidSecurityGroup *types.InvalidSecurityGroupException
		var invalidScheme *types.InvalidSchemeException
		var tooManyTags *types.TooManyTagsException
		var duplicateTagKeys *types.DuplicateTagKeysException
		var resourceInUse *types.ResourceInUseException
		var allocationIDNotFound *types.AllocationIdNotFoundException
		var availabilityZoneNotSupported *types.AvailabilityZoneNotSupportedException
		var operationNotPermitted *types.OperationNotPermittedException

		if errors.As(err, &duplicateLoadBalancerName) {
			fmt.Printf("%T: %s\n", duplicateLoadBalancerName, duplicateLoadBalancerName.Error())
		} else if errors.As(err, &tooManyLoadBalancers) {
			fmt.Printf("%T: %s\n", tooManyLoadBalancers, tooManyLoadBalancers.Error())
		} else if errors.As(err, &invalidConfigurationRequest) {
			fmt.Printf("%T: %s\n", invalidConfigurationRequest, invalidConfigurationRequest.Error())
		} else if errors.As(err, &subnetNotFound) {
			fmt.Printf("%T: %s\n", subnetNotFound, subnetNotFound.Error())
		} else if errors.As(err, &invalidSubnet) {
			fmt.Printf("%T: %s\n", invalidSubnet, invalidSubnet.Error())
		} else if errors.As(err, &invalidSecurityGroup) {
			fmt.Printf("%T: %s\n", invalidSecurityGroup, invalidSecurityGroup.Error())
		} else if errors.As(err, &invalidScheme) {
			fmt.Printf("%T: %s\n", invalidScheme, invalidScheme.Error())
		} else if errors.As(err, &tooManyTags) {
			fmt.Printf("%T: %s\n", tooManyTags, tooManyTags.Error())
		} else if errors.As(err, &duplicateTagKeys) {
			fmt.Printf("%T: %s\n", duplicateTagKeys, duplicateTagKeys.Error())
		} else if errors.As(err, &resourceInUse) {
			fmt.Printf("%T: %s\n", resourceInUse, resourceInUse.Error())
		} else if errors.As(err, &allocationIDNotFound) {
			fmt.Printf("%T: %s\n", allocationIDNotFound, allocationIDNotFound.Error())
		} else if errors.As(err, &availabilityZoneNotSupported) {
			fmt.Printf("%T: %s\n", availabilityZoneNotSupported, availabilityZoneNotSupported.Error())
		} else if errors.As(err, &operationNotPermitted) {
			fmt.Printf("%T: %s\n", operationNotPermitted, operationNotPermitted.Error())
		} else {
			fmt.Printf("%T: %s\n", err, err.Error())
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

func (creds *CREDS) terminateLoadBalancerTargetGroup(ctx context.Context, userName string) error {
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
			fmt.Printf("%T: %s\n", targetGroupNotFoundException, targetGroupNotFoundException.Error())
		} else {
			Config.Logger.Printf("Error describing target group: %s", err.Error())
		}
		return err
	}

	input := &elasticloadbalancingv2.DeleteTargetGroupInput{
		TargetGroupArn: tgArn.TargetGroups[0].TargetGroupArn,
	}

	_, err = svc.DeleteTargetGroup(ctx, input)
	if err != nil {
		var resourceInUseException *types.ResourceInUseException
		if errors.As(err, &resourceInUseException) {
			fmt.Printf("%T: %s\n", resourceInUseException, resourceInUseException.Error())
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

	result, err := svc.DescribeLoadBalancers(ctx, getInput)
	if err != nil {
		var loadBalancerNotFoundException *types.LoadBalancerNotFoundException
		if errors.As(err, &loadBalancerNotFoundException) {
			fmt.Printf("%T: %s\n", loadBalancerNotFoundException, loadBalancerNotFoundException.Error())
		}
		return err
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
