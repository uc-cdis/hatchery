package hatchery

import (
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ecs"
)

type NetworkInfo struct {
	vpc            *ec2.DescribeVpcsOutput
	subnets        *ec2.DescribeSubnetsOutput
	securityGroups *ec2.DescribeSecurityGroupsOutput
	routeTable     *ec2.DescribeRouteTablesOutput
}

func (creds *CREDS) describeWorkspaceNetwork(userName string) (*NetworkInfo, error) {
	svc := ec2.New(session.New(&aws.Config{
		Credentials: creds.creds,
		Region:      aws.String("us-east-1"),
	}))

	vpcname := userToResourceName(userName, "service") + "-" + strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-vpc"
	vpcInput := &ec2.DescribeVpcsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String(vpcname)},
			},
			{
				Name:   aws.String("tag:Environment"),
				Values: []*string{aws.String(os.Getenv("GEN3_ENDPOINT"))},
			},
		},
	}

	vpcs, err := svc.DescribeVpcs(vpcInput)
	if err != nil {
		return nil, err
	}
	// TODO: BETTER ERROR HANDLING HERE!!
	if len(vpcs.Vpcs) == 0 {
		return nil, fmt.Errorf("No existing vpcs found.")
	}

	subnetInput := &ec2.DescribeSubnetsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []*string{aws.String(*vpcs.Vpcs[0].VpcId)},
			},
		},
	}

	subnets, err := svc.DescribeSubnets(subnetInput)
	if err != nil {
		return nil, err
	}

	securityGroupInput := ec2.DescribeSecurityGroupsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []*string{aws.String(*vpcs.Vpcs[0].VpcId)},
			},
			{
				Name:   aws.String("group-name"),
				Values: []*string{aws.String("ws-security-group")},
			},
			{
				Name:   aws.String("tag:Environment"),
				Values: []*string{aws.String(os.Getenv("GEN3_ENDPOINT"))},
			},
		},
	}
	securityGroup, err := svc.DescribeSecurityGroups(&securityGroupInput)
	if err != nil {
		return nil, err
	}
	// Create security group if it doesn't exist
	if len(securityGroup.SecurityGroups) == 0 {
		createSecurityGroupInput := ec2.CreateSecurityGroupInput{
			GroupName: aws.String("ws-security-group"),
			TagSpecifications: []*ec2.TagSpecification{
				{
					ResourceType: aws.String("security-group"),
					Tags: []*ec2.Tag{
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
			VpcId:       aws.String(*vpcs.Vpcs[0].VpcId),
			Description: aws.String("Security group for workspaces running in ECS"),
		}

		newSecurityGroup, err := svc.CreateSecurityGroup(&createSecurityGroupInput)
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("Create Security Group: %s", *newSecurityGroup.GroupId)

		// TODO: Make this secure. Right now it's wide open
		ingressRules := ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: newSecurityGroup.GroupId,
			IpPermissions: []*ec2.IpPermission{
				{
					UserIdGroupPairs: []*ec2.UserIdGroupPair{
						{
							GroupId: newSecurityGroup.GroupId,
						},
					},
					IpProtocol: aws.String("tcp"),
					// Port-range
					FromPort: aws.Int64(2049),
					ToPort:   aws.Int64(2049),
				},
				{
					IpProtocol: aws.String("tcp"),
					IpRanges: []*ec2.IpRange{
						{
							CidrIp:      aws.String("0.0.0.0/0"),
							Description: aws.String("All IPv4"),
						},
					},
					Ipv6Ranges: []*ec2.Ipv6Range{
						{
							CidrIpv6:    aws.String("::/0"),
							Description: aws.String("All IPv6"),
						},
					},
					// Port-range
					FromPort: aws.Int64(80),
					ToPort:   aws.Int64(80),
				},
				{
					IpProtocol: aws.String("tcp"),
					// Port-range
					FromPort: aws.Int64(0),
					ToPort:   aws.Int64(65535),
					IpRanges: []*ec2.IpRange{
						{
							CidrIp:      vpcs.Vpcs[0].CidrBlock,
							Description: aws.String("All IPv4"),
						},
					},
				},
			},
		}
		_, err = svc.AuthorizeSecurityGroupIngress(&ingressRules)
		if err != nil {
			return nil, err
		}

		securityGroup, _ = svc.DescribeSecurityGroups(&securityGroupInput)
	}

	routeTableInput := &ec2.DescribeRouteTablesInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []*string{vpcs.Vpcs[0].VpcId},
			},
			{
				Name:   aws.String("association.main"),
				Values: []*string{aws.String("true")},
			},
		},
	}
	routeTable, err := svc.DescribeRouteTables(routeTableInput)
	if err != nil {
		return nil, err
	}

	networkInfo := NetworkInfo{
		vpc:            vpcs,
		subnets:        subnets,
		securityGroups: securityGroup,
		routeTable:     routeTable,
	}
	return &networkInfo, nil
}

func (creds *CREDS) networkConfig(userName string) (ecs.NetworkConfiguration, error) {

	networkInfo, err := creds.describeWorkspaceNetwork(userName)
	if err != nil {
		return ecs.NetworkConfiguration{}, err
	}

	networkConfig := ecs.NetworkConfiguration{
		AwsvpcConfiguration: &ecs.AwsVpcConfiguration{
			// Whether the task's elastic network interface receives a public IP address.
			// The default value is DISABLED.
			AssignPublicIp: aws.String("ENABLED"),
			// The IDs of the security groups associated with the task or service. If you
			// do not specify a security group, the default security group for the VPC is
			// used. There is a limit of 5 security groups that can be specified per AwsVpcConfiguration.
			//
			// All specified security groups must be from the same VPC.
			SecurityGroups: []*string{aws.String(*networkInfo.securityGroups.SecurityGroups[0].GroupId)},
			//
			// The IDs of the subnets associated with the task or service. There is a limit
			// of 16 subnets that can be specified per AwsVpcConfiguration.
			//
			// All specified subnets must be from the same VPC.
			//
			// Subnets is a required field
			Subnets: []*string{aws.String(*networkInfo.subnets.Subnets[0].SubnetId)},
			// contains filtered or unexported fields
		},
	}
	return networkConfig, nil
}
