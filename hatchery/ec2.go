package hatchery

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ecs"
)

// TODO: return a signle struct of all these instead.
func (creds *CREDS) describeDefaultNetwork() (defaultVpc *ec2.DescribeVpcsOutput, defaultSubnets *ec2.DescribeSubnetsOutput, securityGroups *ec2.DescribeSecurityGroupsOutput, err error) {
	svc := ec2.New(session.New(&aws.Config{
		Credentials: creds.creds,
		Region:      aws.String("us-east-1"),
	}))
	vpcInput := &ec2.DescribeVpcsInput{
		Filters: []*ec2.Filter{
			&ec2.Filter{
				Name:   aws.String("is-default"),
				Values: []*string{aws.String("true")},
			},
		},
	}

	vpcs, err := svc.DescribeVpcs(vpcInput)
	if err != nil {
		return &ec2.DescribeVpcsOutput{}, &ec2.DescribeSubnetsOutput{}, &ec2.DescribeSecurityGroupsOutput{}, err
	}

	subnetInput := &ec2.DescribeSubnetsInput{
		Filters: []*ec2.Filter{
			&ec2.Filter{
				Name:   aws.String("vpc-id"),
				Values: []*string{aws.String(*vpcs.Vpcs[0].VpcId)},
			},
		},
	}

	subnets, err := svc.DescribeSubnets(subnetInput)
	if err != nil {
		return &ec2.DescribeVpcsOutput{}, &ec2.DescribeSubnetsOutput{}, &ec2.DescribeSecurityGroupsOutput{}, err
	}

	securityGroupInput := ec2.DescribeSecurityGroupsInput{
		Filters: []*ec2.Filter{
			&ec2.Filter{
				Name:   aws.String("vpc-id"),
				Values: []*string{aws.String(*vpcs.Vpcs[0].VpcId)},
			},
			&ec2.Filter{
				Name:   aws.String("group-name"),
				Values: []*string{aws.String("ws-security-group")},
			},
		},
	}
	securityGroup, err := svc.DescribeSecurityGroups(&securityGroupInput)
	if err != nil {
		return nil, nil, nil, err
	}
	// Create security group if it doesn't exist
	if len(securityGroup.SecurityGroups) == 0 {
		createSecurityGroupInput := ec2.CreateSecurityGroupInput{
			GroupName: aws.String("ws-security-group"),
			TagSpecifications: []*ec2.TagSpecification{
				{
					Tags: []*ec2.Tag{
						{
							Key:   aws.String("Name"),
							Value: aws.String("ws-security-group"),
						},
					},
				},
			},
			VpcId:       aws.String(*vpcs.Vpcs[0].VpcId),
			Description: aws.String("Security group for workspaces running in ECS"),
		}

		newSecurityGroup, err := svc.CreateSecurityGroup(&createSecurityGroupInput)
		if err != nil {
			return nil, nil, nil, err
		}
		Config.Logger.Printf("Create Security Grouo: %s", *newSecurityGroup.GroupId)

		// TODO: Make this secure. Right now it's wide open
		ingressRules := ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: newSecurityGroup.GroupId,
			IpPermissions: []*ec2.IpPermission{
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
					ToPort: aws.Int64(80),
				},
			},
		}
		_, err = svc.AuthorizeSecurityGroupIngress(&ingressRules)
		if err != nil {
			return nil, nil, nil, err
		}

		securityGroup, _ = svc.DescribeSecurityGroups(&securityGroupInput)
	}

	return vpcs, subnets, securityGroup, nil
}

func (creds *CREDS) networkConfig() (ecs.NetworkConfiguration, error) {

	_, subnets, securityGroup, err := creds.describeDefaultNetwork()
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
			SecurityGroups: []*string{aws.String(*securityGroup.SecurityGroups[0].GroupId)},
			//
			// The IDs of the subnets associated with the task or service. There is a limit
			// of 16 subnets that can be specified per AwsVpcConfiguration.
			//
			// All specified subnets must be from the same VPC.
			//
			// Subnets is a required field
			Subnets: []*string{aws.String(*subnets.Subnets[0].SubnetId)},
			// contains filtered or unexported fields
		},
	}
	return networkConfig, nil
}
