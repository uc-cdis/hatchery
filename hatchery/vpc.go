package hatchery

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"

	"github.com/apparentlymart/go-cidr/cidr"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

func setupVPC(userName string) (*string, error) {
	ctx := context.TODO()

	Config.Logger.Printf("Setting up VPC for user %s", userName)
	pm, err := getCurrentPayModel(userName)
	if err != nil {
		return nil, err
	}

	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	cfg, err := config.LoadDefaultConfig(ctx,
		// TODO: Make this configurable
		config.WithRegion("us-east-1"),
	)
	if err != nil {
		return nil, err
	}
	svc := NewSVC(cfg, roleARN)

	ec2Remote := ec2.NewFromConfig(cfg, func(o *ec2.Options) {
		o.Credentials = svc.creds
	})

	// Subnets
	// TODO: make base CIDR configurable?
	cidrstring := "192.160.0.0/12"
	_, IPNet, _ := net.ParseCIDR(cidrstring)
	subnet, err := cidr.Subnet(IPNet, 14, pm.Subnet)
	if err != nil {
		return nil, err
	}
	subnetString := subnet.String()

	Config.Logger.Printf("Using subnet: %s for user %s. Make sure this does not overlap with other users", subnetString, userName)

	// VPC stuff
	vpcname := userToResourceName(userName, "service") + "-" + strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-vpc"
	descVPCInput := &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("cidr"),
				Values: []string{subnetString},
			},
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
	vpc, err := ec2Remote.DescribeVpcs(ctx, descVPCInput)
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

func createVPC(cidr string, vpcname string, svc *ec2.Client) (*ec2.CreateVpcOutput, error) {
	ctx := context.TODO()

	createVPCInput := &ec2.CreateVpcInput{
		CidrBlock: aws.String(cidr),
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeVpc,
				Tags: []ec2types.Tag{
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
	vpc, err := svc.CreateVpc(ctx, createVPCInput)
	if err != nil {
		return nil, err
	}

	_, err = svc.ModifyVpcAttribute(ctx, &ec2.ModifyVpcAttributeInput{
		EnableDnsHostnames: &ec2types.AttributeBooleanValue{
			Value: aws.Bool(true),
		},
		VpcId: vpc.Vpc.VpcId,
	})
	if err != nil {
		return nil, err
	}

	return vpc, nil
}

func createSubnet(vpccidr string, vpcid string, svc *ec2.Client) error {
	ctx := context.TODO()

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
	_, err = svc.CreateSubnet(ctx, createSubnet1Input)
	if err != nil {
		return err
	}
	_, err = svc.CreateSubnet(ctx, createSubnet2Input)
	if err != nil {
		return err
	}
	return nil

}

func createInternetGW(name string, vpcid string, svc *ec2.Client) (*string, error) {
	ctx := context.TODO()

	Config.Logger.Printf("Setting up internet Gateway for VPC: %s", vpcid)
	describeInternetGWInput := &ec2.DescribeInternetGatewaysInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("attachment.vpc-id"),
				Values: []string{vpcid},
			},
		},
	}
	exIgw, err := svc.DescribeInternetGateways(ctx, describeInternetGWInput)
	if err != nil {
		return nil, err
	}
	if len(exIgw.InternetGateways) == 0 {
		Config.Logger.Printf("No existing gateways found. Creating internet gateway for VPC: %s", vpcid)
		createInternetGWInput := &ec2.CreateInternetGatewayInput{
			TagSpecifications: []ec2types.TagSpecification{
				{
					ResourceType: ec2types.ResourceTypeInternetGateway,
					Tags: []ec2types.Tag{
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
		igw, err := svc.CreateInternetGateway(ctx, createInternetGWInput)
		if err != nil {
			return nil, err
		}
		_, err = svc.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{
			InternetGatewayId: igw.InternetGateway.InternetGatewayId,
			VpcId:             &vpcid,
		})
		if err != nil {
			return nil, err
		}
		routeTable, err := svc.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
			Filters: []ec2types.Filter{
				{
					Name:   aws.String("vpc-id"),
					Values: []string{vpcid},
				},
			},
		})
		if err != nil {
			return nil, err
		}
		_, err = svc.CreateRoute(ctx, &ec2.CreateRouteInput{
			DestinationCidrBlock: aws.String("0.0.0.0/0"),
			GatewayId:            igw.InternetGateway.InternetGatewayId,
			RouteTableId:         routeTable.RouteTables[0].RouteTableId,
		})
		if err != nil {
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				if apiErr.ErrorCode() == "RouteAlreadyExists" {
					// the route already exists, replace it
					Config.Logger.Print("Debug: Route already exists, replacing it")
					_, err = svc.ReplaceRoute(ctx, &ec2.ReplaceRouteInput{
						DestinationCidrBlock: aws.String("0.0.0.0/0"),
						GatewayId:            igw.InternetGateway.InternetGatewayId,
						RouteTableId:         routeTable.RouteTables[0].RouteTableId,
					})
					if err != nil {
						return nil, err
					}
				} else {
					return nil, err
				}
			} else {
				return nil, err
			}
		}
		return igw.InternetGateway.InternetGatewayId, nil
	} else {
		if len(exIgw.InternetGateways[0].Attachments) == 0 {
			Config.Logger.Printf("Existing gateway found but not attached to IGW. Attaching internet gateway for VPC: %s", vpcid)
			_, err = svc.AttachInternetGateway(ctx, &ec2.AttachInternetGatewayInput{
				InternetGatewayId: exIgw.InternetGateways[0].InternetGatewayId,
				VpcId:             &vpcid,
			})
			if err != nil {
				return nil, err
			}
		}

		routeTable, err := svc.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
			Filters: []ec2types.Filter{
				{
					Name:   aws.String("vpc-id"),
					Values: []string{vpcid},
				},
			},
		})
		if err != nil {
			return nil, err
		}

		_, err = svc.CreateRoute(ctx, &ec2.CreateRouteInput{
			DestinationCidrBlock: aws.String("0.0.0.0/0"),
			GatewayId:            exIgw.InternetGateways[0].InternetGatewayId,
			RouteTableId:         routeTable.RouteTables[0].RouteTableId,
		})
		if err != nil {
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				if apiErr.ErrorCode() == "RouteAlreadyExists" {
					// the route already exists, replace it
					Config.Logger.Print("Debug: Route already exists, replacing it")
					_, err = svc.ReplaceRoute(ctx, &ec2.ReplaceRouteInput{
						DestinationCidrBlock: aws.String("0.0.0.0/0"),
						GatewayId:            exIgw.InternetGateways[0].InternetGatewayId,
						RouteTableId:         routeTable.RouteTables[0].RouteTableId,
					})
					if err != nil {
						return nil, err
					}
				} else {
					return nil, err
				}
			} else {
				return nil, err
			}
		}
		return exIgw.InternetGateways[0].InternetGatewayId, nil
	}

}

func VPCRoutes(remote_network_info *NetworkInfo, main_network_info *NetworkInfo, tgwId *string, ec2_remote *ec2.Client, ec2_local *ec2.Client, teardown bool) error {
	ctx := context.TODO()

	if !teardown {
		for _, cidr := range main_network_info.vpcCidrBlocks {
			Config.Logger.Printf("Creating route to %s in remote VPC route table %s", *cidr, *main_network_info.routeTable.RouteTables[0].RouteTableId)
			err := createVPCRoute(remote_network_info.routeTable.RouteTables[0].RouteTableId, cidr, tgwId, ec2_remote)
			if err != nil {
				return err
			}
		}

		err := createVPCRoute(main_network_info.routeTable.RouteTables[0].RouteTableId, remote_network_info.vpc.Vpcs[0].CidrBlock, tgwId, ec2_local)
		if err != nil {
			return err
		}

		return nil
	} else {
		// Delete Routes for all VPC CIDR's
		for _, cidr := range main_network_info.vpcCidrBlocks {
			Config.Logger.Printf("Deleting route %s from remote VPC %s", *cidr, *remote_network_info.vpc.Vpcs[0].VpcId)
			err := deleteVPCRoute(remote_network_info.routeTable.RouteTables[0].RouteTableId, cidr, ec2_remote)
			if err != nil {
				return err
			}
		}

		Config.Logger.Printf("Deleting route %s from main VPC %s", *remote_network_info.vpc.Vpcs[0].CidrBlock, *main_network_info.vpc.Vpcs[0].VpcId)
		localDeleteRouteInput := &ec2.DeleteRouteInput{
			DestinationCidrBlock: remote_network_info.vpc.Vpcs[0].CidrBlock,
			RouteTableId:         main_network_info.routeTable.RouteTables[0].RouteTableId,
		}

		_, err := ec2_local.DeleteRoute(ctx, localDeleteRouteInput)
		if err != nil {
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				switch apiErr.ErrorCode() {
				case "InvalidRoute.NotFound":
					// Route already deleted, we are happy :)
					Config.Logger.Printf("Route %s to remote vpc was already deleted from main VPC %s", *remote_network_info.vpc.Vpcs[0].CidrBlock, *main_network_info.vpc.Vpcs[0].VpcId)
					return nil
				}
			}
			return err
		}
		return nil
	}
}

func createVPCRoute(routeTableId *string, cidrBlock *string, tgwId *string, svc *ec2.Client) error {
	ctx := context.TODO()

	exRemoteRouteInput := &ec2.DescribeRouteTablesInput{
		RouteTableIds: []string{*routeTableId},
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("route.destination-cidr-block"),
				Values: []string{*cidrBlock},
			},
		},
	}
	exRemoteRoute, err := svc.DescribeRouteTables(ctx, exRemoteRouteInput)
	if err != nil {
		return err
	}

	if len(exRemoteRoute.RouteTables) != 0 {
		remoteDeleteRouteInput := &ec2.DeleteRouteInput{
			DestinationCidrBlock: cidrBlock,
			RouteTableId:         routeTableId,
		}
		_, err := svc.DeleteRoute(ctx, remoteDeleteRouteInput)
		if err != nil {
			return err
		}
	}

	remoteCreateRouteInput := &ec2.CreateRouteInput{
		DestinationCidrBlock: cidrBlock,
		RouteTableId:         routeTableId,
		TransitGatewayId:     tgwId,
	}

	_, err = svc.CreateRoute(ctx, remoteCreateRouteInput)
	if err != nil {
		return err
	}
	Config.Logger.Printf("Route to %s added.", *cidrBlock)
	return nil
}

func deleteVPCRoute(routeTableId *string, cidrBlock *string, svc *ec2.Client) error {
	ctx := context.TODO()

	delRouteInput := &ec2.DeleteRouteInput{
		DestinationCidrBlock: cidrBlock,
		RouteTableId:         routeTableId,
	}
	_, err := svc.DeleteRoute(ctx, delRouteInput)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "InvalidRoute.NotFound":
				// Route already deleted, we are happy :)
				return nil
			}
		}
		return err
	}
	return nil
}
