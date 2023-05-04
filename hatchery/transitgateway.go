package hatchery

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
)

// This function sets up the transit gateway between the account hatchery is running in, and the account workspaces will run.
func setupTransitGateway(userName string) error {
	// Create new AWS session to be used by this function
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))

	pm, err := getCurrentPayModel(userName)
	if err != nil {
		return err
	}

	Config.Logger.Infow("Setting up transit gateway in main account",
		"username", userName,
	)
	tgwid, tgwarn, tgwRouteTableId, err := createTransitGateway(sess, userName)
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
	ramArn, err := shareTransitGateway(sess, *tgwarn, pm.AWSAccountId)
	if err != nil {
		return err
	}

	// Accept transit gateway share in remote account
	err = acceptTransitGatewayShare(pm, *tgwarn, sess, ramArn)
	if err != nil {
		return err
	}

	Config.Logger.Infow("Setting up remote account",
		"username", userName,
		"paymodel", pm.Name,
	)
	err = setupRemoteAccount(userName, false)
	if err != nil {
		return fmt.Errorf("failed to setup remote account: %s", err.Error())
	}
	return nil
}

func createLocalTransitGatewayAttachment(userName string, tgwid string, tgwRouteTableId *string) error {
	vpcid := os.Getenv("GEN3_VPCID")

	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))

	// ec2 session to main AWS account.
	ec2Local := ec2.New(sess)
	// Create Transit Gateway Attachment in local VPC
	Config.Logger.Infow("Creating transitgateway attachment in local VPC",
		"username", userName,
		"vpcid", vpcid,
	)
	tgwAttachment, err := createTransitGatewayAttachments(ec2Local, vpcid, tgwid, true, nil, userName)
	if err != nil {
		return err
	}

	Config.Logger.Infow("Transit Gateway attachment created",
		"username", userName,
		"attachment", *tgwAttachment,
	)

	// Create Transit Gateway Route Table
	_, err = TGWRoutes(userName, tgwRouteTableId, tgwAttachment, ec2Local, true, false, nil)
	if err != nil {
		Config.Logger.Errorw("Failed to set up transit gateway route table",
			"username", userName,
			"error", err.Error(),
		)
		return err
	}

	return nil
}

func teardownTransitGateway(userName string) error {

	Config.Logger.Infow("Terminating remote transit gateway attachment",
		"username", userName,
	)
	err := setupRemoteAccount(userName, true)
	if err != nil {
		return err
	}

	return nil

}

func describeMainNetwork(vpcid string, svc *ec2.EC2) (*NetworkInfo, error) {
	networkInfo := NetworkInfo{}
	vpcInput := &ec2.DescribeVpcsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String(vpcid)},
			},
		},
	}
	vpc, err := svc.DescribeVpcs(vpcInput)
	if err != nil {
		return nil, err
	}
	if len(vpc.Vpcs) == 0 {
		return nil, fmt.Errorf("no VPC's found in hub account: %s", vpc)
	}
	subnetInput := &ec2.DescribeSubnetsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []*string{aws.String(*vpc.Vpcs[0].VpcId)},
			},
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String("eks_private_0"), aws.String("eks_private_1"), aws.String("eks_private_2")},
			},
		},
	}
	subnets, err := svc.DescribeSubnets(subnetInput)
	if err != nil {
		return nil, err
	}
	if len(subnets.Subnets) == 0 {
		return nil, fmt.Errorf("no subnets found: %s", subnets)
	}

	routeTableInput := &ec2.DescribeRouteTablesInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []*string{vpc.Vpcs[0].VpcId},
			},
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String("eks_private")},
			},
		},
	}
	routeTable, err := svc.DescribeRouteTables(routeTableInput)
	if err != nil {
		return nil, err
	}

	networkInfo.vpc = vpc
	networkInfo.subnets = subnets
	networkInfo.routeTable = routeTable
	return &networkInfo, nil
}

func createTransitGateway(sess *session.Session, userName string) (tgwid *string, tgwarn *string, tgwRouteTableId *string, err error) {

	// ec2 session to main AWS account.
	ec2Local := ec2.New(sess)

	tgwName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-tgw"
	// Check for existing transit gateway
	exTg, err := ec2Local.DescribeTransitGateways(&ec2.DescribeTransitGatewaysInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("state"),
				Values: []*string{aws.String("available"), aws.String("pending")},
			},
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String(tgwName)},
			},
		},
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to DescribeTransitGateways: %s", err.Error())
	}

	// Create Transit Gateway if it doesn't exist
	if len(exTg.TransitGateways) == 0 {

		Config.Logger.Infow("No existing transit gateway found. Creating a new transit gateway...")
		tgwName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-tgw"
		tg, err := ec2Local.CreateTransitGateway(&ec2.CreateTransitGatewayInput{
			DryRun:      aws.Bool(false),
			Description: aws.String("Transit gateway to connect external VPC's"),
			Options: &ec2.TransitGatewayRequestOptions{
				AutoAcceptSharedAttachments:  aws.String("enable"),
				DefaultRouteTablePropagation: aws.String("disable"),
			},
			TagSpecifications: []*ec2.TagSpecification{
				{
					ResourceType: aws.String("transit-gateway"),
					Tags: []*ec2.Tag{
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

		Config.Logger.Infow("Transit gateway created",
			"transit_gateway_id", *tg.TransitGateway.TransitGatewayId,
			"username", userName,
		)

		return tg.TransitGateway.TransitGatewayId, tg.TransitGateway.TransitGatewayArn, tg.TransitGateway.Options.AssociationDefaultRouteTableId, nil
	} else {
		// Config.Logger.Print("Existing transit gateway found. Skipping creation...")
		Config.Logger.Infow("Existing transit gateway found. Skipping creation...",
			"transit_gateway_id", *exTg.TransitGateways[len(exTg.TransitGateways)-1].TransitGatewayId,
			"username", userName,
		)
		return exTg.TransitGateways[len(exTg.TransitGateways)-1].TransitGatewayId, exTg.TransitGateways[len(exTg.TransitGateways)-1].TransitGatewayArn, exTg.TransitGateways[len(exTg.TransitGateways)-1].Options.AssociationDefaultRouteTableId, nil
	}
}

func createTransitGatewayAttachments(svc *ec2.EC2, vpcid string, tgwid string, local bool, sess *CREDS, userName string) (*string, error) {

	Config.Logger.Infow("Creating transit gateway attachment for VPC",
		"vpc_id", vpcid,
		"username", userName,
	)
	// Check for existing transit gateway
	tgInput := &ec2.DescribeTransitGatewaysInput{
		TransitGatewayIds: []*string{aws.String(tgwid)},
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("state"),
				Values: []*string{aws.String("available"), aws.String("pending")},
			},
		},
	}
	exTg, err := svc.DescribeTransitGateways(tgInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case "InvalidTransitGatewayID.NotFound":
				// Sleep for 10 seconds before trying again..
				time.Sleep(10 * time.Second)
				_, err = svc.DescribeTransitGateways(tgInput)
				if err != nil {
					return nil, fmt.Errorf("cannot DescribeTransitGateways again: %s", err.Error())
				}
			default:
				return nil, fmt.Errorf("cannot DescribeTransitGateways: %s", err.Error())
			}
		}
		return nil, err
	}
	for *exTg.TransitGateways[0].State != "available" {
		Config.Logger.Infow("TransitGateway is not ready yet. Waiting for 10 seconds...",
			"state", *exTg.TransitGateways[0].State,
			"username", userName,
		)
		// sleep for 10 sec
		time.Sleep(10 * time.Second)
		exTg, _ = svc.DescribeTransitGateways(tgInput)
	}
	var networkInfo *NetworkInfo
	if local {
		networkInfo, err = describeMainNetwork(vpcid, svc)
	} else {
		networkInfo, err = sess.describeWorkspaceNetwork(userName)
	}
	if err != nil {
		return nil, fmt.Errorf("Failed to get network info: %s", err)
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
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("tag:Environment"),
				Values: []*string{aws.String(os.Getenv("GEN3_ENDPOINT"))},
			},
			{
				Name:   aws.String("resource-id"),
				Values: []*string{networkInfo.vpc.Vpcs[0].VpcId},
			},
			{
				Name:   aws.String("state"),
				Values: []*string{aws.String("available"), aws.String("pending")},
			},
		},
	}
	exTgwAttachment, err := svc.DescribeTransitGatewayAttachments(exTgwAttachmentInput)
	if err != nil {
		return nil, err
	}
	if len(exTgwAttachment.TransitGatewayAttachments) == 0 {
		// Create the transit gateway attachment
		tgwAttachmentInput := &ec2.CreateTransitGatewayVpcAttachmentInput{
			TransitGatewayId: exTg.TransitGateways[0].TransitGatewayId,
			VpcId:            networkInfo.vpc.Vpcs[len(networkInfo.vpc.Vpcs)-1].VpcId,
			TagSpecifications: []*ec2.TagSpecification{
				{
					ResourceType: aws.String("transit-gateway-attachment"),
					Tags: []*ec2.Tag{
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
		Config.Logger.Infow("Creating a new local transit gateway attachment as none was found.",
			"username", userName,
		)
		for i := range networkInfo.subnets.Subnets {
			tgwAttachmentInput.SubnetIds = append(tgwAttachmentInput.SubnetIds, networkInfo.subnets.Subnets[i].SubnetId)
		}
		tgwAttachment, err := svc.CreateTransitGatewayVpcAttachment(tgwAttachmentInput)
		if err != nil {
			return nil, fmt.Errorf("cannot create transitgatewayattachment: %s", err.Error())
		}

		Config.Logger.Infow("Created transitgatewayattachment",
			"username", userName,
			"transitGatewayAttachment", *tgwAttachment.TransitGatewayVpcAttachment.TransitGatewayAttachmentId,
		)
		return tgwAttachment.TransitGatewayVpcAttachment.TransitGatewayAttachmentId, nil
	} else {

		Config.Logger.Infow("Using existing local transit gateway attachment",
			"username", userName,
			"transitGatewayAttachment", *exTgwAttachment.TransitGatewayAttachments[0].TransitGatewayAttachmentId,
		)
		return exTgwAttachment.TransitGatewayAttachments[0].TransitGatewayAttachmentId, nil
	}
}

func deleteTransitGatewayAttachment(svc *ec2.EC2, tgwid string, userName string) (*string, error) {
	tgwAttachmentName := userToResourceName(userName, "service") + "tgwa"
	exTgwAttachmentInput := &ec2.DescribeTransitGatewayAttachmentsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("transit-gateway-id"),
				Values: []*string{aws.String(tgwid)},
			},
			{
				Name:   aws.String("state"),
				Values: []*string{aws.String("available"), aws.String("pending")},
			},
			{
				Name: aws.String("tag:Name"),
				Values: []*string{
					aws.String(tgwAttachmentName),
				},
			},
			{
				Name: aws.String("tag:Environment"),
				Values: []*string{
					aws.String(os.Getenv("GEN3_ENDPOINT")),
				},
			},
		},
	}
	exTgwAttachment, err := svc.DescribeTransitGatewayAttachments(exTgwAttachmentInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case "InvalidTransitGatewayID.NotFound":
				// No TGW attachment found, we are happy :)
				return nil, nil
			}
		} else {

			Config.Logger.Errorw("Error describing transit gateway attachment",
				"username", userName,
				"error", err.Error(),
			)
			return nil, err
		}
	}
	if len(exTgwAttachment.TransitGatewayAttachments) == 0 {
		// No transit gateway attachment found, we are happy :)

		Config.Logger.Debug("No transit gateway attachment found. We are happy :)",
			"username", userName,
		)

		return nil, nil
	}

	delTGWAttachmentInput := &ec2.DeleteTransitGatewayVpcAttachmentInput{
		TransitGatewayAttachmentId: exTgwAttachment.TransitGatewayAttachments[0].TransitGatewayAttachmentId,
	}
	delTGWAttachment, err := svc.DeleteTransitGatewayVpcAttachment(delTGWAttachmentInput)
	if err != nil {
		return nil, err
	}

	return delTGWAttachment.TransitGatewayVpcAttachment.TransitGatewayAttachmentId, nil
}

func setupRemoteAccount(userName string, teardown bool) error {
	pm, err := getCurrentPayModel(userName)
	if err != nil {
		return err
	}
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))
	svc := NewSVC(sess, roleARN)

	ec2Local := ec2.New(sess)
	ec2Remote := ec2.New(session.Must(session.NewSession(&aws.Config{
		Credentials: svc.creds,
		Region:      aws.String("us-east-1"),
	})))

	vpcid := os.Getenv("GEN3_VPCID")
	exTgInput := &ec2.DescribeTransitGatewaysInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("state"),
				Values: []*string{aws.String("available"), aws.String("pending")},
			},
			{
				Name:   aws.String("tag:Environment"),
				Values: []*string{aws.String(os.Getenv("GEN3_ENDPOINT"))},
			},
		},
	}
	exTg, err := ec2Local.DescribeTransitGateways(exTgInput)
	if err != nil {
		return err
	}
	for len(exTg.TransitGateways) == 0 {

		Config.Logger.Warnw("Waiting to find ex_tgw",
			"username", userName,
		)
		// err := svc.acceptTGWShare()
		// if err != nil {
		// 	return err
		// }
		exTg, err = ec2Local.DescribeTransitGateways(exTgInput)
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
			Config.Logger.Errorw("Cannot create remote TransitGatewayAttachment",
				"username", userName,
				"error", err.Error(),
			)
			return fmt.Errorf("Cannot create remote TransitGatewayAttachment: %s", err.Error())
		}

		Config.Logger.Debug("tgw_attachment",
			"username", userName,
			"tgw_attachment", *tgw_attachment,
		)
	}

	// setup Transit Gateway Route Table
	_, err = TGWRoutes(userName, exTg.TransitGateways[0].Options.AssociationDefaultRouteTableId, tgw_attachment, ec2Local, false, teardown, &svc)
	if err != nil {
		return fmt.Errorf("Cannot create TGW Route: %s", err.Error())
	}
	// setup VPC Route Table
	err = VPCRoutes(networkInfo, mainNetworkInfo, exTg.TransitGateways[0].TransitGatewayId, ec2Remote, ec2Local, teardown)
	if err != nil {
		return fmt.Errorf("failed to create vpc routes: %s", err.Error())
	}

	return nil
}

func TGWRoutes(userName string, tgwRoutetableId *string, tgwAttachmentId *string, svc *ec2.EC2, local bool, teardown bool, sess *CREDS) (*string, error) {
	var networkInfo *NetworkInfo
	vpcid := os.Getenv("GEN3_VPCID")
	err := *new(error)
	if local {
		networkInfo, err = describeMainNetwork(vpcid, svc)
		if err != nil {
			return nil, err
		}
	} else {
		networkInfo, err = sess.describeWorkspaceNetwork(userName)
		if err != nil {
			return nil, err
		}
	}
	tgwAttachmentInput := &ec2.DescribeTransitGatewayAttachmentsInput{
		TransitGatewayAttachmentIds: []*string{tgwAttachmentId},
	}
	tgwAttachment, err := svc.DescribeTransitGatewayAttachments(tgwAttachmentInput)
	if err != nil {
		return nil, fmt.Errorf("error DescribeTransitGatewayAttachments: %s", err.Error())
	}
	if teardown {

		delRouteInput := &ec2.DeleteTransitGatewayRouteInput{
			DestinationCidrBlock:       networkInfo.vpc.Vpcs[0].CidrBlock,
			TransitGatewayRouteTableId: tgwRoutetableId,
		}
		_, err := svc.DeleteTransitGatewayRoute(delRouteInput)
		if err != nil {
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				case "InvalidRoute.NotFound":
					// Route already deleted, we are happy :)
					return nil, nil
				}
			}
			return nil, err
		}
		return delRouteInput.TransitGatewayRouteTableId, nil
	} else {
		for *tgwAttachment.TransitGatewayAttachments[0].State != "available" {

			Config.Logger.Warnw("Transit Gateway Attachment is not ready",
				"username", userName,
				"state", *tgwAttachment.TransitGatewayAttachments[0].State,
			)
			tgwAttachment, err = svc.DescribeTransitGatewayAttachments(tgwAttachmentInput)
			if err != nil {
				return nil, err
			}
			time.Sleep(5 * time.Second)
		}

		exRoutesInput := &ec2.SearchTransitGatewayRoutesInput{
			TransitGatewayRouteTableId: tgwRoutetableId,
			Filters: []*ec2.Filter{
				{
					Name:   aws.String("route-search.subnet-of-match"),
					Values: []*string{networkInfo.vpc.Vpcs[0].CidrBlock},
				},
			},
		}
		exRoutes, err := svc.SearchTransitGatewayRoutes(exRoutesInput)
		if err != nil {
			// log error

			Config.Logger.Errorw("error SearchTransitGatewayRoutes",
				"username", userName,
				"error", err.Error(),
			)
			return nil, err
		}

		if len(exRoutes.Routes) == 1 {
			if local {
				// Delete route only if it's blackhole route
				if *exRoutes.Routes[0].State == "blackhole" {

					Config.Logger.Infow("Route is blackhole, deleting",
						"username", userName,
						"route", *exRoutes.Routes[0].DestinationCidrBlock,
						"state", *exRoutes.Routes[0].State,
					)
					delRouteInput := &ec2.DeleteTransitGatewayRouteInput{
						DestinationCidrBlock:       networkInfo.vpc.Vpcs[0].CidrBlock,
						TransitGatewayRouteTableId: tgwRoutetableId,
					}
					_, err := svc.DeleteTransitGatewayRoute(delRouteInput)
					if err != nil {
						return nil, err
					}
				} else {
					Config.Logger.Infow("Route already exists",
						"username", userName,
						"route", *networkInfo.vpc.Vpcs[0].CidrBlock,
					)
					return exRoutesInput.TransitGatewayRouteTableId, nil
				}
			} else {
				delRouteInput := &ec2.DeleteTransitGatewayRouteInput{
					DestinationCidrBlock:       networkInfo.vpc.Vpcs[0].CidrBlock,
					TransitGatewayRouteTableId: tgwRoutetableId,
				}
				_, err := svc.DeleteTransitGatewayRoute(delRouteInput)
				if err != nil {
					return nil, err
				}
			}
		}
		tgRouteInput := &ec2.CreateTransitGatewayRouteInput{
			TransitGatewayRouteTableId: tgwRoutetableId,
			DestinationCidrBlock:       networkInfo.vpc.Vpcs[0].CidrBlock,
			TransitGatewayAttachmentId: tgwAttachmentId,
		}
		tgRoute, err := svc.CreateTransitGatewayRoute(tgRouteInput)
		if err != nil {
			return nil, err
		}

		return tgRoute.Route.PrefixListId, nil
	}
}

func VPCRoutes(remote_network_info *NetworkInfo, main_network_info *NetworkInfo, tgwId *string, ec2_remote *ec2.EC2, ec2_local *ec2.EC2, teardown bool) error {
	if !teardown {
		exRemoteRouteInput := &ec2.DescribeRouteTablesInput{
			RouteTableIds: []*string{remote_network_info.routeTable.RouteTables[0].RouteTableId},
			Filters: []*ec2.Filter{
				{
					Name:   aws.String("route.destination-cidr-block"),
					Values: []*string{main_network_info.vpc.Vpcs[0].CidrBlock},
				},
			},
		}
		exRemoteRoute, err := ec2_remote.DescribeRouteTables(exRemoteRouteInput)
		if err != nil {
			return err
		}

		if len(exRemoteRoute.RouteTables) != 0 {
			remoteDeleteRouteInput := &ec2.DeleteRouteInput{
				DestinationCidrBlock: main_network_info.vpc.Vpcs[0].CidrBlock,
				RouteTableId:         remote_network_info.routeTable.RouteTables[0].RouteTableId,
			}
			_, err := ec2_remote.DeleteRoute(remoteDeleteRouteInput)
			if err != nil {
				return err
			}
		}

		remoteCreateRouteInput := &ec2.CreateRouteInput{
			DestinationCidrBlock: main_network_info.vpc.Vpcs[0].CidrBlock,
			RouteTableId:         remote_network_info.routeTable.RouteTables[0].RouteTableId,
			TransitGatewayId:     tgwId,
		}

		_, err = ec2_remote.CreateRoute(remoteCreateRouteInput)
		if err != nil {
			return err
		}

		Config.Logger.Infow("Route added to remote VPC",
			"routeTableId", *remote_network_info.routeTable.RouteTables[0].RouteTableId,
			"destinationCidrBlock", *remote_network_info.vpc.Vpcs[0].CidrBlock,
			"transitGatewayId", *tgwId,
		)

		localCreateRouteInput := &ec2.CreateRouteInput{
			DestinationCidrBlock: remote_network_info.vpc.Vpcs[0].CidrBlock,
			RouteTableId:         main_network_info.routeTable.RouteTables[0].RouteTableId,
			TransitGatewayId:     tgwId,
		}

		localRoute, err := ec2_local.CreateRoute(localCreateRouteInput)
		if err != nil {
			return err
		}

		Config.Logger.Infow("Route added to local VPC",
			"routeTableId", *main_network_info.routeTable.RouteTables[0].RouteTableId,
			"destinationCidrBlock", *remote_network_info.vpc.Vpcs[0].CidrBlock,
			"transitGatewayId", *tgwId,
			"localRoute", localRoute,
		)
		return nil
	} else {
		Config.Logger.Debug("Deleting Routes for remote VPC",
			"vpcId", *remote_network_info.vpc.Vpcs[0].VpcId,
			"cidrBlock", *remote_network_info.vpc.Vpcs[0].CidrBlock,
		)
		remoteDeleteRouteInput := &ec2.DeleteRouteInput{
			DestinationCidrBlock: main_network_info.vpc.Vpcs[0].CidrBlock,
			RouteTableId:         remote_network_info.routeTable.RouteTables[0].RouteTableId,
		}
		_, err := ec2_remote.DeleteRoute(remoteDeleteRouteInput)
		if err != nil {
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				case "InvalidRoute.NotFound":
					return nil
				}
			}
			return err
		}
		localDeleteRouteInput := &ec2.DeleteRouteInput{
			DestinationCidrBlock: remote_network_info.vpc.Vpcs[0].CidrBlock,
			RouteTableId:         main_network_info.routeTable.RouteTables[0].RouteTableId,
		}

		_, err = ec2_local.DeleteRoute(localDeleteRouteInput)
		if err != nil {
			return err
		}
		return nil
	}
}
