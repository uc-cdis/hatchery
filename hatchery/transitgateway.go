package hatchery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

// This function sets up the transit gateway between the account hatchery is running in, and the account workspaces will run.
func setupTransitGateway(userName string) error {
	ctx := context.TODO()

	// Load AWS config to be used by this function
	cfg, err := config.LoadDefaultConfig(ctx,
		// TODO: Make this configurable
		config.WithRegion("us-east-1"),
	)
	if err != nil {
		return err
	}

	pm, err := getCurrentPayModel(userName)
	if err != nil {
		return err
	}

	Config.Logger.Printf("Setting up transit gateway in main account")
	tgwid, tgwarn, tgwRouteTableId, err := createTransitGateway(cfg, userName)
	if err != nil {
		return fmt.Errorf("error creating transit gateway: %s", err.Error())
	}

	// This transit gateway attachment connects the main account to the remote account
	// This is needed once per environment, instead of once per user.
	err = createLocalTransitGatewayAttachment(userName, *tgwid, tgwRouteTableId)
	if err != nil {
		return fmt.Errorf("error creating local transit gateway attachment: %s", err.Error())
	}

	// Make sure transit gateway is shared with the remote account
	ramArn, err := shareTransitGateway(cfg, *tgwarn, pm.AWSAccountId)
	if err != nil {
		return err
	}

	// Accept transit gateway share in remote account
	err = acceptTransitGatewayShare(pm, cfg, ramArn)
	if err != nil {
		return err
	}

	Config.Logger.Printf("Setting up remote account ")
	err = setupRemoteAccount(userName, false)
	if err != nil {
		return fmt.Errorf("failed to setup remote account: %s", err.Error())
	}
	return nil
}

func createLocalTransitGatewayAttachment(userName string, tgwid string, tgwRouteTableId *string) error {
	ctx := context.TODO()

	vpcid := os.Getenv("GEN3_VPCID")

	cfg, err := config.LoadDefaultConfig(ctx,
		// TODO: Make this configurable
		config.WithRegion("us-east-1"),
	)
	if err != nil {
		return err
	}

	// ec2 client to main AWS account.
	ec2Local := ec2.NewFromConfig(cfg)
	// Create Transit Gateway Attachment in local VPC
	// Config.Logger.Printf("Creating tgw attachment in local VPC: %s", vpcid)
	tgwAttachment, err := createTransitGatewayAttachments(ec2Local, vpcid, tgwid, true, nil, userName)
	if err != nil {
		return err
	}
	Config.Logger.Printf("Attachment created: %s", *tgwAttachment)

	// Create Transit Gateway Route Table
	err = TGWRoutes(userName, tgwRouteTableId, tgwAttachment, ec2Local, true, false, nil)
	if err != nil {
		// Log error
		Config.Logger.Printf("Failed to create TGW route table: %s", err.Error())
		return err
	}

	return nil
}

func teardownTransitGateway(userName string) error {
	Config.Logger.Printf("Terminating remote transit gateway attachment for user %s\n", userName)
	err := setupRemoteAccount(userName, true)
	if err != nil {
		return err
	}

	return nil

}

func describeMainNetwork(vpcid string, svc *ec2.Client) (*NetworkInfo, error) {
	ctx := context.TODO()

	networkInfo := NetworkInfo{}
	vpcInput := &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []string{vpcid},
			},
		},
	}
	vpc, err := svc.DescribeVpcs(ctx, vpcInput)
	if err != nil {
		return nil, err
	}
	if len(vpc.Vpcs) == 0 {
		return nil, fmt.Errorf("no VPC's found in hub account: %v", vpc)
	}

	// Get VPC CIDR blocks

	for _, cidr := range vpc.Vpcs[0].CidrBlockAssociationSet {
		// append to networkInfo struct
		networkInfo.vpcCidrBlocks = append(networkInfo.vpcCidrBlocks, cidr.CidrBlock)
	}

	subnetInput := &ec2.DescribeSubnetsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{aws.ToString(vpc.Vpcs[0].VpcId)},
			},
			{
				Name:   aws.String("tag:Name"),
				Values: []string{"eks_private_0", "eks_private_1", "eks_private_2"},
			},
		},
	}
	subnets, err := svc.DescribeSubnets(ctx, subnetInput)
	if err != nil {
		return nil, err
	}
	if len(subnets.Subnets) == 0 {
		return nil, fmt.Errorf("no subnets found: %v", subnets)
	}

	routeTableInput := &ec2.DescribeRouteTablesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{aws.ToString(vpc.Vpcs[0].VpcId)},
			},
			{
				Name:   aws.String("tag:Name"),
				Values: []string{"eks_private"},
			},
		},
	}
	routeTable, err := svc.DescribeRouteTables(ctx, routeTableInput)
	if err != nil {
		return nil, err
	}

	networkInfo.vpc = vpc
	networkInfo.subnets = subnets
	networkInfo.routeTable = routeTable
	return &networkInfo, nil
}

func createTransitGateway(cfg aws.Config, userName string) (tgwid *string, tgwarn *string, tgwRouteTableId *string, err error) {
	ctx := context.TODO()

	// ec2 client to main AWS account.
	ec2Local := ec2.NewFromConfig(cfg)

	tgwName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-tgw"

	// Check for existing transit gateway
	exTg, err := ec2Local.DescribeTransitGateways(ctx, &ec2.DescribeTransitGatewaysInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("state"),
				Values: []string{"available", "pending"},
			},
			{
				Name:   aws.String("tag:Name"),
				Values: []string{tgwName},
			},
		},
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to DescribeTransitGateways: %s", err.Error())
	}

	// Create Transit Gateway if it doesn't exist
	if len(exTg.TransitGateways) == 0 {
		Config.Logger.Printf("No transit gateway found. Creating one...")
		tgwName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-tgw"
		tg, err := ec2Local.CreateTransitGateway(ctx, &ec2.CreateTransitGatewayInput{
			DryRun:      aws.Bool(false),
			Description: aws.String("Transit gateway to connect external VPC's"),
			Options: &ec2types.TransitGatewayRequestOptions{
				AutoAcceptSharedAttachments:  ec2types.AutoAcceptSharedAttachmentsValueEnable,
				DefaultRouteTablePropagation: ec2types.DefaultRouteTablePropagationValueDisable,
			},
			TagSpecifications: []ec2types.TagSpecification{
				{
					ResourceType: ec2types.ResourceTypeTransitGateway,
					Tags: []ec2types.Tag{
						{
							Key:   aws.String("Name"),
							Value: aws.String(tgwName),
						},
						{
							Key:   aws.String("Environment"),
							Value: aws.String(os.Getenv("GEN3_ENDPOINT")),
						},
					},
				},
			},
		})
		if err != nil {
			return nil, nil, nil, err
		}
		Config.Logger.Printf("Transit gateway created: %s", *tg.TransitGateway.TransitGatewayId)

		return tg.TransitGateway.TransitGatewayId, tg.TransitGateway.TransitGatewayArn, tg.TransitGateway.Options.AssociationDefaultRouteTableId, nil
	} else {
		Config.Logger.Print("Existing transit gateway found. Skipping creation...")
		return exTg.TransitGateways[len(exTg.TransitGateways)-1].TransitGatewayId, exTg.TransitGateways[len(exTg.TransitGateways)-1].TransitGatewayArn, exTg.TransitGateways[len(exTg.TransitGateways)-1].Options.AssociationDefaultRouteTableId, nil
	}
}

func createTransitGatewayAttachments(svc *ec2.Client, vpcid string, tgwid string, local bool, sess *CREDS, userName string) (*string, error) {
	ctx := context.TODO()

	Config.Logger.Printf("Creating transit gateway attachment for VPC: %s", vpcid)
	// Check for existing transit gateway
	tgInput := &ec2.DescribeTransitGatewaysInput{
		TransitGatewayIds: []string{tgwid},
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("state"),
				Values: []string{"available", "pending"},
			},
		},
	}
	var exTg *ec2.DescribeTransitGatewaysOutput
	var err error
	maxRetries := 10
	retryInterval := 10 * time.Second

	for i := 0; i < maxRetries; i++ {
		exTg, err = svc.DescribeTransitGateways(ctx, tgInput)
		if err == nil {
			// Successfully described the Transit Gateway
			break
		}

		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "InvalidTransitGatewayID.NotFound":
				// Sleep for the retry interval before trying again
				Config.Logger.Printf("TransitGateway not found, retrying in %s", retryInterval.String())
				time.Sleep(retryInterval)
			default:
				return nil, fmt.Errorf("cannot DescribeTransitGateways: %s", err.Error())
			}
		} else {
			// Some other error occurred, return it immediately
			return nil, fmt.Errorf("cannot DescribeTransitGateways: %s", err.Error())
		}

		// If we've reached the maximum number of retries, return an error
		if i == maxRetries-1 {
			return nil, fmt.Errorf("maximum number of retries reached")
		}
	}

	for exTg.TransitGateways[0].State != ec2types.TransitGatewayStateAvailable {
		Config.Logger.Printf("TransitGateway is in state: %s ...  Waiting for 10 seconds", exTg.TransitGateways[0].State)
		// sleep for 10 sec
		time.Sleep(10 * time.Second)
		exTg, _ = svc.DescribeTransitGateways(ctx, tgInput)
	}
	var networkInfo *NetworkInfo
	if local {
		networkInfo, err = describeMainNetwork(vpcid, svc)
	} else {
		networkInfo, err = sess.describeWorkspaceNetwork(userName)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get network info: %s", err)
	}

	tgwAttachmentName := ""
	if local {
		// Name the local tgwAttachment after the environment, instead of the user.
		// This is shared between all users in the environment.
		tgwAttachmentName = os.Getenv("GEN3_ENDPOINT") + "-tgwa"
	} else {
		tgwAttachmentName = userToResourceName(userName, "service") + "tgwa"
	}

	exTgwAttachmentInput := &ec2.DescribeTransitGatewayAttachmentsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("tag:Environment"),
				Values: []string{os.Getenv("GEN3_ENDPOINT")},
			},
			{
				Name:   aws.String("resource-id"),
				Values: []string{aws.ToString(networkInfo.vpc.Vpcs[0].VpcId)},
			},
			{
				Name:   aws.String("state"),
				Values: []string{"available", "pending"},
			},
		},
	}
	exTgwAttachment, err := svc.DescribeTransitGatewayAttachments(ctx, exTgwAttachmentInput)
	if err != nil {
		return nil, err
	}
	if len(exTgwAttachment.TransitGatewayAttachments) == 0 {
		// Create the transit gateway attachment
		Config.Logger.Printf("Local transitgateway attachment not found, creating new one")
		tgwAttachmentInput := &ec2.CreateTransitGatewayVpcAttachmentInput{
			TransitGatewayId: exTg.TransitGateways[0].TransitGatewayId,
			VpcId:            networkInfo.vpc.Vpcs[len(networkInfo.vpc.Vpcs)-1].VpcId,
			TagSpecifications: []ec2types.TagSpecification{
				{
					ResourceType: ec2types.ResourceTypeTransitGatewayAttachment,
					Tags: []ec2types.Tag{
						{
							Key:   aws.String("Name"),
							Value: aws.String(tgwAttachmentName),
						},
						{
							Key:   aws.String("Environment"),
							Value: aws.String(os.Getenv("GEN3_ENDPOINT")),
						},
					},
				},
			},
		}
		for i := range networkInfo.subnets.Subnets {
			tgwAttachmentInput.SubnetIds = append(tgwAttachmentInput.SubnetIds, aws.ToString(networkInfo.subnets.Subnets[i].SubnetId))
		}
		tgwAttachment, err := svc.CreateTransitGatewayVpcAttachment(ctx, tgwAttachmentInput)
		if err != nil {
			return nil, fmt.Errorf("cannot create transitgatewayattachment: %s", err.Error())
		}
		Config.Logger.Printf("Created transitgatewayattachment: %s", *tgwAttachment.TransitGatewayVpcAttachment.TransitGatewayAttachmentId)
		return tgwAttachment.TransitGatewayVpcAttachment.TransitGatewayAttachmentId, nil
	} else {
		Config.Logger.Printf("Local transitgateway attachment found, using existing one")
		return exTgwAttachment.TransitGatewayAttachments[0].TransitGatewayAttachmentId, nil
	}
}

func deleteTransitGatewayAttachment(svc *ec2.Client, tgwid string, userName string) (*string, error) {
	ctx := context.TODO()

	tgwAttachmentName := userToResourceName(userName, "service") + "tgwa"
	exTgwAttachmentInput := &ec2.DescribeTransitGatewayAttachmentsInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("transit-gateway-id"),
				Values: []string{tgwid},
			},
			{
				Name:   aws.String("state"),
				Values: []string{"available", "pending"},
			},
			{
				Name: aws.String("tag:Name"),
				Values: []string{
					tgwAttachmentName,
				},
			},
			{
				Name: aws.String("tag:Environment"),
				Values: []string{
					os.Getenv("GEN3_ENDPOINT"),
				},
			},
		},
	}
	exTgwAttachment, err := svc.DescribeTransitGatewayAttachments(ctx, exTgwAttachmentInput)
	if err != nil {
		Config.Logger.Printf("Error: %s", err.Error())
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "InvalidTransitGatewayID.NotFound":
				// No TGW attachment found, we are happy :)
				return nil, nil
			}
		} else {
			Config.Logger.Printf("Error: %s", err.Error())
			return nil, err
		}
	}
	if len(exTgwAttachment.TransitGatewayAttachments) == 0 {
		// No transit gateway attachment found, we are happy :)
		Config.Logger.Printf("No TGW attachment found, we are happy :)")
		return nil, nil
	}

	delTGWAttachmentInput := &ec2.DeleteTransitGatewayVpcAttachmentInput{
		TransitGatewayAttachmentId: exTgwAttachment.TransitGatewayAttachments[0].TransitGatewayAttachmentId,
	}
	delTGWAttachment, err := svc.DeleteTransitGatewayVpcAttachment(ctx, delTGWAttachmentInput)
	if err != nil {
		return nil, err
	}

	return delTGWAttachment.TransitGatewayVpcAttachment.TransitGatewayAttachmentId, nil
}

func setupRemoteAccount(userName string, teardown bool) error {
	ctx := context.TODO()

	pm, err := getCurrentPayModel(userName)
	if err != nil {
		return err
	}
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	cfg, err := config.LoadDefaultConfig(ctx,
		// TODO: Make this configurable
		config.WithRegion("us-east-1"),
	)
	if err != nil {
		return err
	}
	svc := NewSVC(cfg, roleARN)

	ec2Local := ec2.NewFromConfig(cfg)
	ec2Remote := ec2.NewFromConfig(cfg, func(o *ec2.Options) {
		o.Credentials = svc.creds
		o.Region = "us-east-1"
	})

	vpcid := os.Getenv("GEN3_VPCID")
	exTgInput := &ec2.DescribeTransitGatewaysInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("state"),
				Values: []string{"available", "pending"},
			},
			{
				Name:   aws.String("tag:Environment"),
				Values: []string{os.Getenv("GEN3_ENDPOINT")},
			},
		},
	}
	exTg, err := ec2Local.DescribeTransitGateways(ctx, exTgInput)
	if err != nil {
		return err
	}
	for len(exTg.TransitGateways) == 0 {
		Config.Logger.Printf("Waiting to find ex_tgw")
		// err := svc.acceptTGWShare()
		// if err != nil {
		// 	return err
		// }
		exTg, err = ec2Local.DescribeTransitGateways(ctx, exTgInput)
		if err != nil {
			return err
		}
		time.Sleep(5 * time.Second)
	}
	networkInfo, err := svc.describeWorkspaceNetwork(userName)
	if err != nil {
		return err
	}
	vpc := *networkInfo.vpc

	mainNetworkInfo, err := describeMainNetwork(vpcid, ec2Local)
	if err != nil {
		return err
	}
	var tgw_attachment *string
	if teardown {
		tgw_attachment, err = deleteTransitGatewayAttachment(ec2Remote, *exTg.TransitGateways[0].TransitGatewayId, userName)
		if err != nil {
			return err
		}
	} else {
		tgw_attachment, err = createTransitGatewayAttachments(ec2Remote, *vpc.Vpcs[0].VpcId, *exTg.TransitGateways[0].TransitGatewayId, false, &svc, userName)
		if err != nil {
			return fmt.Errorf("cannot create remote TransitGatewayAttachment: %s", err.Error())
		}
		Config.Logger.Printf("tgw_attachment: %s", *tgw_attachment)
	}

	// setup Transit Gateway Route Table
	err = TGWRoutes(userName, exTg.TransitGateways[0].Options.AssociationDefaultRouteTableId, tgw_attachment, ec2Local, false, teardown, &svc)
	if err != nil {
		return fmt.Errorf("cannot create TGW Route: %s", err.Error())
	}
	// setup VPC Route Table
	err = VPCRoutes(networkInfo, mainNetworkInfo, exTg.TransitGateways[0].TransitGatewayId, ec2Remote, ec2Local, teardown)
	if err != nil {
		return fmt.Errorf("failed to create vpc routes: %s", err.Error())
	}

	return nil
}

func TGWRoutes(userName string, tgwRoutetableId *string, tgwAttachmentId *string, svc *ec2.Client, local bool, teardown bool, sess *CREDS) error {
	ctx := context.TODO()

	var networkInfo *NetworkInfo
	vpcid := os.Getenv("GEN3_VPCID")
	err := *new(error)
	if local {
		networkInfo, err = describeMainNetwork(vpcid, svc)
		if err != nil {
			return err
		}
	} else {
		networkInfo, err = sess.describeWorkspaceNetwork(userName)
		if err != nil {
			return err
		}
	}
	tgwAttachmentInput := &ec2.DescribeTransitGatewayAttachmentsInput{
		TransitGatewayAttachmentIds: []string{*tgwAttachmentId},
	}
	tgwAttachment, err := svc.DescribeTransitGatewayAttachments(ctx, tgwAttachmentInput)
	if err != nil {
		return fmt.Errorf("error DescribeTransitGatewayAttachments: %s", err.Error())
	}
	if teardown {
		//  delete routes to all VPC CIDR blocks
		for _, cidr := range networkInfo.vpcCidrBlocks {
			Config.Logger.Printf("Deleting route to %s in transit gateway route table %s", *cidr, *tgwRoutetableId)
			err := deleteTGWRoute(tgwRoutetableId, cidr, svc)
			if err != nil {
				return err
			}
		}

		return nil
	} else {
		for tgwAttachment.TransitGatewayAttachments[0].State != ec2types.TransitGatewayAttachmentStateAvailable {
			Config.Logger.Printf("Transit Gateway Attachment is not ready. State is: %s", tgwAttachment.TransitGatewayAttachments[0].State)
			tgwAttachment, err = svc.DescribeTransitGatewayAttachments(ctx, tgwAttachmentInput)
			if err != nil {
				return err
			}
			time.Sleep(5 * time.Second)
		}

		//  create routes to all VPC CIDR blocks
		for _, cidr := range networkInfo.vpcCidrBlocks {
			Config.Logger.Printf("Creating route to %s in transit gateway route table %s", *cidr, *tgwRoutetableId)
			err := createTGWRoute(tgwRoutetableId, cidr, tgwAttachment.TransitGatewayAttachments[0].TransitGatewayAttachmentId, local, svc)
			if err != nil {
				return err
			}
		}

		return nil
	}
}

func deleteTGWRoute(routeTableId *string, cidrBlock *string, svc *ec2.Client) error {
	ctx := context.TODO()

	Config.Logger.Printf("Deleting route to %s from route table %s", *cidrBlock, *routeTableId)
	delRouteInput := &ec2.DeleteTransitGatewayRouteInput{
		DestinationCidrBlock:       cidrBlock,
		TransitGatewayRouteTableId: routeTableId,
	}
	_, err := svc.DeleteTransitGatewayRoute(ctx, delRouteInput)
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

func createTGWRoute(routeTableId *string, cidrBlock *string, attachmentId *string, local bool, svc *ec2.Client) error {
	ctx := context.TODO()

	// Check if route already exists.
	// If it exists make sure it's not blackholed, and if it is delete it.
	// If it doesn't exist, create the route in the transit gateway	route table
	exRoutesInput := &ec2.SearchTransitGatewayRoutesInput{
		TransitGatewayRouteTableId: routeTableId,
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("route-search.subnet-of-match"),
				Values: []string{aws.ToString(cidrBlock)},
			},
		},
	}
	exRoutes, err := svc.SearchTransitGatewayRoutes(ctx, exRoutesInput)
	if err != nil {
		// log error
		Config.Logger.Printf("error SearchTransitGatewayRoutes: %s", err.Error())
		return err
	}

	if len(exRoutes.Routes) == 1 {
		if local {
			// Delete route only if it's blackhole route
			if exRoutes.Routes[0].State == ec2types.TransitGatewayRouteStateBlackhole {
				Config.Logger.Printf("Route is blackhole, deleting")
				delRouteInput := &ec2.DeleteTransitGatewayRouteInput{
					DestinationCidrBlock:       cidrBlock,
					TransitGatewayRouteTableId: routeTableId,
				}
				_, err := svc.DeleteTransitGatewayRoute(ctx, delRouteInput)
				if err != nil {
					return err
				}
			} else {
				Config.Logger.Printf("Route to %s already exists in routeTable %s", *cidrBlock, *routeTableId)
				return nil
			}
		} else {
			delRouteInput := &ec2.DeleteTransitGatewayRouteInput{
				DestinationCidrBlock:       cidrBlock,
				TransitGatewayRouteTableId: routeTableId,
			}
			_, err := svc.DeleteTransitGatewayRoute(ctx, delRouteInput)
			if err != nil {
				return err
			}
		}
	}
	tgRouteInput := &ec2.CreateTransitGatewayRouteInput{
		TransitGatewayRouteTableId: routeTableId,
		DestinationCidrBlock:       cidrBlock,
		TransitGatewayAttachmentId: attachmentId,
	}
	_, err = svc.CreateTransitGatewayRoute(ctx, tgRouteInput)
	if err != nil {
		return err
	}
	return nil
}
