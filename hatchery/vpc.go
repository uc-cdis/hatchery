package hatchery

import (
	"net"
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

	// subnet stuff
	// TODO: make base CIDR configurable
	cidrstring := "192.165.0.0/16"
	_, IPNet, _ := net.ParseCIDR(cidrstring)
	subnet, err := cidr.Subnet(IPNet, 11, pm.Subnet)
	if err != nil {
		return nil, err
	}
	subnetString := subnet.String()

	// VPC stuff
	vpcname := strings.ReplaceAll(Config.Config.Sidecar.Env["BASE_URL"], ".", "-") + "-vpc"
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

	if len(vpc.Vpcs) == 0 {
		create_vpc, err := createVPC(subnetString, vpcname, ec2_remote)
		if err != nil {
			return nil, err
		}
		return create_vpc.Vpc.CidrBlock, nil
	} else {
		return vpc.Vpcs[0].CidrBlock, nil

	}

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

	err = createSubnet(cidr, *vpc.Vpc.VpcId, svc)
	if err != nil {
		return nil, err
	}
	_, err = createInternetGW(vpcname, *vpc.Vpc.VpcId, svc)
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
		VpcId:     &vpcid,
	}
	createSubnet2Input := &ec2.CreateSubnetInput{
		CidrBlock: aws.String(subnet2_cidr.String()),
		VpcId:     &vpcid,
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

func createInternetGW(name string, vpcid string, svc *ec2.EC2) (*ec2.CreateInternetGatewayOutput, error) {
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

	route, err := svc.CreateRoute(&ec2.CreateRouteInput{
		DestinationCidrBlock: aws.String("0.0.0.0/0"),
		GatewayId:            igw.InternetGateway.InternetGatewayId,
	})
	if err != nil {
		return nil, err
	}
	Config.Logger.Printf("Route: %s", route)
	return igw, nil
}
