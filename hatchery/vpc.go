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

func setupVPC(username string) (*string, error) {
	pm := Config.PayModelMap[username]

	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))

	svc := NewSession(sess, roleARN)

	ec2_remote := ec2.New(session.New(&aws.Config{
		Credentials: svc.creds,
		Region:      aws.String("us-east-1"),
	}))

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
	vpcname := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-vpc"
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
		},
	}
	vpc, err := ec2_remote.DescribeVpcs(descVPCInput)
	if err != nil {
		return nil, err
	}
	// TODO: Check that VPC is configured correctly too, and not just the length
	if len(vpc.Vpcs) == 0 {
		vpc, err := createVPC(subnetString, vpcname, ec2_remote)
		if err != nil {
			return nil, err
		}
		_, err = createInternetGW(vpcname, *vpc.Vpc.VpcId, ec2_remote)
		if err != nil {
			return nil, err
		}
	}
	ex_network, err := svc.describeWorkspaceNetwork()
	if err != nil {
		return nil, err
	}
	_, err = createInternetGW(vpcname, *ex_network.vpc.Vpcs[0].VpcId, ec2_remote)
	if err != nil {
		return nil, err
	}

	// TODO: Check that subnets are configured correctly too, and not just the length
	if len(ex_network.subnets.Subnets) == 0 {
		err = createSubnet(subnetString, *ex_network.vpc.Vpcs[0].VpcId, ec2_remote)
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
				},
			},
		},
	}
	vpc, err := svc.CreateVpc(createVPCInput)
	if err != nil {
		return nil, err
	}

	svc.ModifyVpcAttribute(&ec2.ModifyVpcAttributeInput{
		EnableDnsHostnames: &ec2.AttributeBooleanValue{
			Value: aws.Bool(true),
		},
		VpcId: vpc.Vpc.VpcId,
	})

	return vpc, nil
}

func createSubnet(vpccidr string, vpcid string, svc *ec2.EC2) error {
	_, cidrs, err := net.ParseCIDR(vpccidr)
	if err != nil {
		return err
	}
	subnet1_cidr, err := (cidr.Subnet(cidrs, 1, 0))
	if err != nil {
		panic(err)
	}
	subnet2_cidr, _ := cidr.Subnet(cidrs, 1, 1)
	if err != nil {
		panic(err)
	}

	Config.Logger.Print(cidrs)
	createSubnet1Input := &ec2.CreateSubnetInput{
		CidrBlock: aws.String(subnet1_cidr.String()),
		//TODO: Make this configurable
		AvailabilityZone: aws.String("us-east-1a"),
		VpcId:            &vpcid,
	}
	createSubnet2Input := &ec2.CreateSubnetInput{
		//TODO: Make this configurable
		AvailabilityZone: aws.String("us-east-1b"),
		CidrBlock:        aws.String(subnet2_cidr.String()),
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
	ex_igw, err := svc.DescribeInternetGateways(describeInternetGWInput)
	if err != nil {
		return nil, err
	}
	if len(ex_igw.InternetGateways) == 0 {
		createInternetGWInput := &ec2.CreateInternetGatewayInput{
			TagSpecifications: []*ec2.TagSpecification{
				{
					ResourceType: aws.String("internet-gateway"),
					Tags: []*ec2.Tag{
						{
							Key:   aws.String("Name"),
							Value: aws.String(name),
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
	}
	return ex_igw.InternetGateways[0].InternetGatewayId, nil
}
