package hatchery

import (
	"net"
	"os"
	"strings"

	"github.com/apparentlymart/go-cidr/cidr"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
)

func setupVPC(userName string) (*string, error) {
	pm := Config.PayModelMap[userName]

	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))

	svc := NewSVC(sess, roleARN)

	ec2Remote := ec2.New(session.Must(session.NewSession(&aws.Config{
		Credentials: svc.creds,
		Region:      aws.String("us-east-1"),
	})))

	// Subnets
	// TODO: make base CIDR configurable?
	cidrstring := "192.165.0.0/12"
	_, IPNet, _ := net.ParseCIDR(cidrstring)
	subnet, err := cidr.Subnet(IPNet, 15, pm.Subnet)
	if err != nil {
		return nil, err
	}
	subnetString := subnet.String()

	// VPC stuff
	vpcname := userToResourceName(userName, "service") + "-" + strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-vpc"
	descVPCInput := &ec2.DescribeVpcsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("cidr"),
				Values: []*string{aws.String(subnetString)},
			},
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
	vpc, err := ec2Remote.DescribeVpcs(descVPCInput)
	if err != nil {
		return nil, err
	}
	// TODO: Check that VPC is configured correctly too, and not just the length
	if len(vpc.Vpcs) == 0 {
		vpc, err := createVPC(subnetString, vpcname, ec2Remote)
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("VPC created in remote account")
		_, err = createInternetGW(vpcname, *vpc.Vpc.VpcId, ec2Remote)
		if err != nil {
			return nil, err
		}
	}
	exNetwork, err := svc.describeWorkspaceNetwork(userName)
	if err != nil {
		return nil, err
	}
	_, err = createInternetGW(vpcname, *exNetwork.vpc.Vpcs[0].VpcId, ec2Remote)
	if err != nil {
		return nil, err
	}

	// TODO: Check that subnets are configured correctly too, and not just the length
	if len(exNetwork.subnets.Subnets) == 0 {
		err = createSubnet(subnetString, *exNetwork.vpc.Vpcs[0].VpcId, ec2Remote)
		if err != nil {
			return nil, err
		}
	}

	return nil, nil
}

func createVPC(cidr string, vpcname string, svc *ec2.EC2) (*ec2.CreateVpcOutput, error) {
	createVPCInput := &ec2.CreateVpcInput{
		CidrBlock: aws.String(cidr),
		TagSpecifications: []*ec2.TagSpecification{
			{
				ResourceType: aws.String("vpc"),
				Tags: []*ec2.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(vpcname),
					},
					{
						Key:   aws.String("Environment"),
						Value: aws.String(os.Getenv("GEN3_ENDPOINT")),
					},
				},
			},
		},
	}
	vpc, err := svc.CreateVpc(createVPCInput)
	if err != nil {
		return nil, err
	}

	_, err = svc.ModifyVpcAttribute(&ec2.ModifyVpcAttributeInput{
		EnableDnsHostnames: &ec2.AttributeBooleanValue{
			Value: aws.Bool(true),
		},
		VpcId: vpc.Vpc.VpcId,
	})
	if err != nil {
		return nil, err
	}

	return vpc, nil
}

func createSubnet(vpccidr string, vpcid string, svc *ec2.EC2) error {
	_, cidrs, err := net.ParseCIDR(vpccidr)
	if err != nil {
		return err
	}
	subnet1Cidr, err := (cidr.Subnet(cidrs, 1, 0))
	if err != nil {
		panic(err)
	}
	subnet2Cidr, _ := cidr.Subnet(cidrs, 1, 1)
	if err != nil {
		panic(err)
	}

	Config.Logger.Print(cidrs)
	createSubnet1Input := &ec2.CreateSubnetInput{
		CidrBlock: aws.String(subnet1Cidr.String()),
		//TODO: Make this configurable ?
		AvailabilityZone: aws.String("us-east-1a"),
		VpcId:            &vpcid,
	}
	createSubnet2Input := &ec2.CreateSubnetInput{
		//TODO: Make this configurable ?
		AvailabilityZone: aws.String("us-east-1b"),
		CidrBlock:        aws.String(subnet2Cidr.String()),
		VpcId:            &vpcid,
	}
	_, err = svc.CreateSubnet(createSubnet1Input)
	if err != nil {
		return err
	}
	_, err = svc.CreateSubnet(createSubnet2Input)
	if err != nil {
		return err
	}
	return nil

}

func createInternetGW(name string, vpcid string, svc *ec2.EC2) (*string, error) {
	describeInternetGWInput := &ec2.DescribeInternetGatewaysInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("attachment.vpc-id"),
				Values: []*string{aws.String(vpcid)},
			},
		},
	}
	exIgw, err := svc.DescribeInternetGateways(describeInternetGWInput)
	if err != nil {
		return nil, err
	}
	if len(exIgw.InternetGateways) == 0 {
		createInternetGWInput := &ec2.CreateInternetGatewayInput{
			TagSpecifications: []*ec2.TagSpecification{
				{
					ResourceType: aws.String("internet-gateway"),
					Tags: []*ec2.Tag{
						{
							Key:   aws.String("Name"),
							Value: aws.String(name),
						},
						{
							Key:   aws.String("Environment"),
							Value: aws.String(os.Getenv("GEN3_ENDPOINT")),
						},
					},
				},
			},
		}
		igw, err := svc.CreateInternetGateway(createInternetGWInput)
		if err != nil {
			return nil, err
		}
		_, err = svc.AttachInternetGateway(&ec2.AttachInternetGatewayInput{
			InternetGatewayId: igw.InternetGateway.InternetGatewayId,
			VpcId:             &vpcid,
		})
		if err != nil {
			return nil, err
		}
		routeTable, err := svc.DescribeRouteTables(&ec2.DescribeRouteTablesInput{
			Filters: []*ec2.Filter{
				{
					Name:   aws.String("vpc-id"),
					Values: []*string{&vpcid},
				},
			},
		})
		if err != nil {
			return nil, err
		}
		route, err := svc.CreateRoute(&ec2.CreateRouteInput{
			DestinationCidrBlock: aws.String("0.0.0.0/0"),
			GatewayId:            igw.InternetGateway.InternetGatewayId,
			RouteTableId:         routeTable.RouteTables[0].RouteTableId,
		})
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("Route: %s", route)
		return igw.InternetGateway.InternetGatewayId, nil
	} else {
		if len(exIgw.InternetGateways[0].Attachments) == 0 {
			_, err = svc.AttachInternetGateway(&ec2.AttachInternetGatewayInput{
				InternetGatewayId: exIgw.InternetGateways[0].InternetGatewayId,
				VpcId:             &vpcid,
			})
			if err != nil {
				return nil, err
			}
		}

		routeTable, err := svc.DescribeRouteTables(&ec2.DescribeRouteTablesInput{
			Filters: []*ec2.Filter{
				{
					Name:   aws.String("vpc-id"),
					Values: []*string{&vpcid},
				},
			},
		})
		if err != nil {
			return nil, err
		}

		Config.Logger.Printf("Routes: %s", routeTable.RouteTables[0].Routes)

		route, err := svc.CreateRoute(&ec2.CreateRouteInput{
			DestinationCidrBlock: aws.String("0.0.0.0/0"),
			GatewayId:            exIgw.InternetGateways[0].InternetGatewayId,
			RouteTableId:         routeTable.RouteTables[0].RouteTableId,
		})
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("Route: %s", route)
		return exIgw.InternetGateways[0].InternetGatewayId, nil
	}

}
