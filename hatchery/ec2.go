package hatchery

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

type NetworkInfo struct {
	vpc            *ec2.DescribeVpcsOutput
	vpcCidrBlocks  []*string
	subnets        *ec2.DescribeSubnetsOutput
	securityGroups *ec2.DescribeSecurityGroupsOutput
	routeTable     *ec2.DescribeRouteTablesOutput
}

func (creds *CREDS) describeWorkspaceNetwork(userName string) (*NetworkInfo, error) {
	ctx := context.TODO()
	cfg := aws.Config{Region: "us-east-1", Credentials: creds.creds}
	svc := ec2.NewFromConfig(cfg)

	vpcname := userToResourceName(userName, "service") + "-" + strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-vpc"
	vpcInput := &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []string{vpcname},
			},
			{
				Name:   aws.String("tag:Environment"),
				Values: []string{os.Getenv("GEN3_ENDPOINT")},
			},
		},
	}

	vpcs, err := svc.DescribeVpcs(ctx, vpcInput)
	if err != nil {
		return nil, err
	}
	// TODO: BETTER ERROR HANDLING HERE!!
	if len(vpcs.Vpcs) == 0 {
		return nil, fmt.Errorf("no existing vpcs found")
	}

	subnetInput := &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{aws.ToString(vpcs.Vpcs[0].VpcId)},
			},
		},
	}

	subnets, err := svc.DescribeSubnets(ctx, subnetInput)
	if err != nil {
		return nil, err
	}

	securityGroupInput := ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{aws.ToString(vpcs.Vpcs[0].VpcId)},
			},
			{
				Name:   aws.String("group-name"),
				Values: []string{"ws-security-group"},
			},
			{
				Name:   aws.String("tag:Environment"),
				Values: []string{os.Getenv("GEN3_ENDPOINT")},
			},
		},
	}
	securityGroup, err := svc.DescribeSecurityGroups(ctx, &securityGroupInput)
	if err != nil {
		return nil, err
	}
	// Create security group if it doesn't exist
	if len(securityGroup.SecurityGroups) == 0 {
		createSecurityGroupInput := ec2.CreateSecurityGroupInput{
			GroupName: aws.String("ws-security-group"),
			TagSpecifications: []ec2types.TagSpecification{
				{
					ResourceType: ec2types.ResourceTypeSecurityGroup,
					Tags: []ec2types.Tag{
						{
							Key:   aws.String("Name"),
							Value: aws.String("ws-security-group"),
						},
						{
							Key:   aws.String("Environment"),
							Value: aws.String(os.Getenv("GEN3_ENDPOINT")),
						},
					},
				},
			},
			VpcId:       vpcs.Vpcs[0].VpcId,
			Description: aws.String("Security group for workspaces running in ECS"),
		}

		newSecurityGroup, err := svc.CreateSecurityGroup(ctx, &createSecurityGroupInput)
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("Create Security Group: %s", *newSecurityGroup.GroupId)

		ingressRules := ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: newSecurityGroup.GroupId,
			IpPermissions: []ec2types.IpPermission{
				{
					UserIdGroupPairs: []ec2types.UserIdGroupPair{
						{
							GroupId: newSecurityGroup.GroupId,
						},
					},
					IpProtocol: aws.String("tcp"),
					// Port-range
					FromPort: aws.Int32(2049),
					ToPort:   aws.Int32(2049),
				},
				{
					IpProtocol: aws.String("tcp"),
					IpRanges: []ec2types.IpRange{
						{
							CidrIp:      aws.String("0.0.0.0/0"),
							Description: aws.String("All IPv4"),
						},
					},
					Ipv6Ranges: []ec2types.Ipv6Range{
						{
							CidrIpv6:    aws.String("::/0"),
							Description: aws.String("All IPv6"),
						},
					},
					// Port-range
					FromPort: aws.Int32(80),
					ToPort:   aws.Int32(80),
				},
				{
					IpProtocol: aws.String("tcp"),
					// Port-range
					FromPort: aws.Int32(0),
					ToPort:   aws.Int32(65535),
					IpRanges: []ec2types.IpRange{
						{
							CidrIp:      vpcs.Vpcs[0].CidrBlock,
							Description: aws.String("All IPv4"),
						},
					},
				},
			},
		}
		_, err = svc.AuthorizeSecurityGroupIngress(ctx, &ingressRules)
		if err != nil {
			return nil, err
		}

		securityGroup, _ = svc.DescribeSecurityGroups(ctx, &securityGroupInput)
	}

	routeTableInput := &ec2.DescribeRouteTablesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{aws.ToString(vpcs.Vpcs[0].VpcId)},
			},
			{
				Name:   aws.String("association.main"),
				Values: []string{"true"},
			},
		},
	}
	routeTable, err := svc.DescribeRouteTables(ctx, routeTableInput)
	if err != nil {
		return nil, err
	}

	networkInfo := NetworkInfo{
		vpc:            vpcs,
		subnets:        subnets,
		securityGroups: securityGroup,
		routeTable:     routeTable,
	}
	networkInfo.vpcCidrBlocks = append(networkInfo.vpcCidrBlocks, vpcs.Vpcs[0].CidrBlock)
	return &networkInfo, nil
}

func (creds *CREDS) NetworkConfig(userName string) (ecstypes.NetworkConfiguration, error) {

	networkInfo, err := creds.describeWorkspaceNetwork(userName)
	if err != nil {
		return ecstypes.NetworkConfiguration{}, err
	}

	networkConfig := ecstypes.NetworkConfiguration{
		AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
			// Whether the task's elastic network interface receives a public IP address.
			// The default value is DISABLED.
			AssignPublicIp: ecstypes.AssignPublicIpEnabled,
			// The IDs of the security groups associated with the task or service. If you
			// do not specify a security group, the default security group for the VPC is
			// used. There is a limit of 5 security groups that can be specified per AwsVpcConfiguration.
			//
			// All specified security groups must be from the same VPC.
			SecurityGroups: []string{aws.ToString(networkInfo.securityGroups.SecurityGroups[0].GroupId)},
			//
			// The IDs of the subnets associated with the task or service. There is a limit
			// of 16 subnets that can be specified per AwsVpcConfiguration.
			//
			// All specified subnets must be from the same VPC.
			//
			// Subnets is a required field
			Subnets: []string{aws.ToString(networkInfo.subnets.Subnets[0].SubnetId)},
			// contains filtered or unexported fields
		},
	}
	return networkConfig, nil
}
