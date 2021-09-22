package hatchery

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/elbv2"
)

func (creds *CREDS) createTargetGroup(userName string, vpcId string, svc *elbv2.ELBV2) (*elbv2.CreateTargetGroupOutput, error) {
	input := &elbv2.CreateTargetGroupInput{
		Name:            aws.String(userToResourceName(userName, "service")),
		Port:            aws.Int64(80),
		Protocol:        aws.String("HTTP"),
		VpcId:           aws.String(vpcId),
		TargetType:      aws.String("ip"),
		HealthCheckPath: aws.String("/lw-workspace/proxy/"),
		Matcher: &elbv2.Matcher{
			HttpCode: aws.String("200-499"),
		},
	}

	result, err := svc.CreateTargetGroup(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case elbv2.ErrCodeDuplicateTargetGroupNameException:
				fmt.Println(elbv2.ErrCodeDuplicateTargetGroupNameException, aerr.Error())
			case elbv2.ErrCodeTooManyTargetGroupsException:
				fmt.Println(elbv2.ErrCodeTooManyTargetGroupsException, aerr.Error())
			case elbv2.ErrCodeInvalidConfigurationRequestException:
				fmt.Println(elbv2.ErrCodeInvalidConfigurationRequestException, aerr.Error())
			case elbv2.ErrCodeTooManyTagsException:
				fmt.Println(elbv2.ErrCodeTooManyTagsException, aerr.Error())
			default:
				fmt.Println(aerr.Error())
				return nil, err
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			fmt.Println(err.Error())
			return nil, err
		}
		return nil, err
	}

	return result, nil

}

func (creds *CREDS) setTargetGroupAttributes(svc *elbv2.ELBV2, targetGroupArn string) (*elbv2.ModifyTargetGroupAttributesOutput, error) {
	modifyTargetGroupAttributesInput := &elbv2.ModifyTargetGroupAttributesInput{
		TargetGroupArn: aws.String(targetGroupArn),
		Attributes: []*elbv2.TargetGroupAttribute{
			{
				Key:   aws.String("deregistration_delay.timeout_seconds"),
				Value: aws.String("0"),
			},
		},
	}
	modifyTargetGroup, err := svc.ModifyTargetGroupAttributes(modifyTargetGroupAttributesInput)
	if err != nil {
		return nil, err
	}
	return modifyTargetGroup, nil
}

func (creds *CREDS) createListener(svc *elbv2.ELBV2, loadBalancer string, targetGroup string) (*elbv2.CreateListenerOutput, error) {
	input := &elbv2.CreateListenerInput{
		DefaultActions: []*elbv2.Action{
			{
				TargetGroupArn: aws.String(targetGroup),
				Type:           aws.String("forward"),
			},
		},
		LoadBalancerArn: aws.String(loadBalancer),
		Port:            aws.Int64(80),
		Protocol:        aws.String("HTTP"),
	}

	result, err := svc.CreateListener(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case elbv2.ErrCodeDuplicateListenerException:
				fmt.Println(elbv2.ErrCodeDuplicateListenerException, aerr.Error())
			case elbv2.ErrCodeTooManyListenersException:
				fmt.Println(elbv2.ErrCodeTooManyListenersException, aerr.Error())
			case elbv2.ErrCodeTooManyCertificatesException:
				fmt.Println(elbv2.ErrCodeTooManyCertificatesException, aerr.Error())
			case elbv2.ErrCodeLoadBalancerNotFoundException:
				fmt.Println(elbv2.ErrCodeLoadBalancerNotFoundException, aerr.Error())
			case elbv2.ErrCodeTargetGroupNotFoundException:
				fmt.Println(elbv2.ErrCodeTargetGroupNotFoundException, aerr.Error())
			case elbv2.ErrCodeTargetGroupAssociationLimitException:
				fmt.Println(elbv2.ErrCodeTargetGroupAssociationLimitException, aerr.Error())
			case elbv2.ErrCodeInvalidConfigurationRequestException:
				fmt.Println(elbv2.ErrCodeInvalidConfigurationRequestException, aerr.Error())
			case elbv2.ErrCodeIncompatibleProtocolsException:
				fmt.Println(elbv2.ErrCodeIncompatibleProtocolsException, aerr.Error())
			case elbv2.ErrCodeSSLPolicyNotFoundException:
				fmt.Println(elbv2.ErrCodeSSLPolicyNotFoundException, aerr.Error())
			case elbv2.ErrCodeCertificateNotFoundException:
				fmt.Println(elbv2.ErrCodeCertificateNotFoundException, aerr.Error())
			case elbv2.ErrCodeUnsupportedProtocolException:
				fmt.Println(elbv2.ErrCodeUnsupportedProtocolException, aerr.Error())
			case elbv2.ErrCodeTooManyRegistrationsForTargetIdException:
				fmt.Println(elbv2.ErrCodeTooManyRegistrationsForTargetIdException, aerr.Error())
			case elbv2.ErrCodeTooManyTargetsException:
				fmt.Println(elbv2.ErrCodeTooManyTargetsException, aerr.Error())
			case elbv2.ErrCodeTooManyActionsException:
				fmt.Println(elbv2.ErrCodeTooManyActionsException, aerr.Error())
			case elbv2.ErrCodeInvalidLoadBalancerActionException:
				fmt.Println(elbv2.ErrCodeInvalidLoadBalancerActionException, aerr.Error())
			case elbv2.ErrCodeTooManyUniqueTargetGroupsPerLoadBalancerException:
				fmt.Println(elbv2.ErrCodeTooManyUniqueTargetGroupsPerLoadBalancerException, aerr.Error())
			case elbv2.ErrCodeALPNPolicyNotSupportedException:
				fmt.Println(elbv2.ErrCodeALPNPolicyNotSupportedException, aerr.Error())
			case elbv2.ErrCodeTooManyTagsException:
				fmt.Println(elbv2.ErrCodeTooManyTagsException, aerr.Error())
			default:
				fmt.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			fmt.Println(err.Error())
		}
		return result, nil
	}
	return result, nil
}

func (creds *CREDS) CreateLoadBalancer(userName string) (*elbv2.CreateLoadBalancerOutput, *string, *elbv2.CreateListenerOutput, error) {
	svc := elbv2.New(session.New(&aws.Config{
		Credentials: creds.creds,
		Region:      aws.String("us-east-1"),
	}))

	vpcs, subnets, securityGroups, err := creds.describeDefaultNetwork()
	input := &elbv2.CreateLoadBalancerInput{
		Name:   aws.String(userToResourceName(userName, "service")),
		Scheme: aws.String("internal"),
		SecurityGroups: []*string{
			securityGroups.SecurityGroups[0].GroupId,
		},
		Subnets: []*string{
			subnets.Subnets[0].SubnetId,
			subnets.Subnets[1].SubnetId,
		},
	}

	loadBalancer, err := svc.CreateLoadBalancer(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case elbv2.ErrCodeDuplicateLoadBalancerNameException:
				fmt.Println(elbv2.ErrCodeDuplicateLoadBalancerNameException, aerr.Error())
			case elbv2.ErrCodeTooManyLoadBalancersException:
				fmt.Println(elbv2.ErrCodeTooManyLoadBalancersException, aerr.Error())
			case elbv2.ErrCodeInvalidConfigurationRequestException:
				fmt.Println(elbv2.ErrCodeInvalidConfigurationRequestException, aerr.Error())
			case elbv2.ErrCodeSubnetNotFoundException:
				fmt.Println(elbv2.ErrCodeSubnetNotFoundException, aerr.Error())
			case elbv2.ErrCodeInvalidSubnetException:
				fmt.Println(elbv2.ErrCodeInvalidSubnetException, aerr.Error())
			case elbv2.ErrCodeInvalidSecurityGroupException:
				fmt.Println(elbv2.ErrCodeInvalidSecurityGroupException, aerr.Error())
			case elbv2.ErrCodeInvalidSchemeException:
				fmt.Println(elbv2.ErrCodeInvalidSchemeException, aerr.Error())
			case elbv2.ErrCodeTooManyTagsException:
				fmt.Println(elbv2.ErrCodeTooManyTagsException, aerr.Error())
			case elbv2.ErrCodeDuplicateTagKeysException:
				fmt.Println(elbv2.ErrCodeDuplicateTagKeysException, aerr.Error())
			case elbv2.ErrCodeResourceInUseException:
				fmt.Println(elbv2.ErrCodeResourceInUseException, aerr.Error())
			case elbv2.ErrCodeAllocationIdNotFoundException:
				fmt.Println(elbv2.ErrCodeAllocationIdNotFoundException, aerr.Error())
			case elbv2.ErrCodeAvailabilityZoneNotSupportedException:
				fmt.Println(elbv2.ErrCodeAvailabilityZoneNotSupportedException, aerr.Error())
			case elbv2.ErrCodeOperationNotPermittedException:
				fmt.Println(elbv2.ErrCodeOperationNotPermittedException, aerr.Error())
			default:
				fmt.Println(aerr.Error())
			}
		} else {
			// Print the error, cast err to awserr.Error to get the Code and
			// Message from an error.
			fmt.Println(err.Error())
			return nil, nil, nil, err
		}
		return nil, nil, nil, err
	}

	targetGroup, err := creds.createTargetGroup(userName, *vpcs.Vpcs[0].VpcId, svc)
	if err != nil {
		return nil, nil, nil, err
	}
	_, err = creds.setTargetGroupAttributes(svc, *targetGroup.TargetGroups[0].TargetGroupArn)
	if err != nil {
		return nil, nil, nil, err
	}
	listener, err := creds.createListener(svc, *loadBalancer.LoadBalancers[0].LoadBalancerArn, *targetGroup.TargetGroups[0].TargetGroupArn)
	if err != nil {
		return nil, nil, nil, err
	}
	return loadBalancer, targetGroup.TargetGroups[0].TargetGroupArn, listener, nil
}
