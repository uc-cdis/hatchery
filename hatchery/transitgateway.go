package hatchery

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ram"
)

func setupTransitGateway(userName string) error {
	_, err := createTransitGateway(userName)
	if err != nil {
		return fmt.Errorf("error creating transit gateway: %s", err.Error())
	}
	Config.Logger.Printf("Setting up remote account ")
	err = setupRemoteAccount(userName, false)
	if err != nil {
		return fmt.Errorf("failed to setup remote account: %s", err.Error())
	}

	return nil
}

func teardownTransitGateway(userName string) error {
	err := setupRemoteAccount(userName, true)
	if err != nil {
		return err
	}

	return nil

}

// TODO: Change the name of this function to match HUB/SPOKE model
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
				Values: []*string{aws.String("main")},
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

func createTransitGateway(userName string) (*string, error) {
	pm := Config.PayModelMap[userName]
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))

	// ec2 session to main AWS account.
	ec2Local := ec2.New(sess)

	vpcid := os.Getenv("GEN3_VPCID")
	Config.Logger.Printf("VPCID: %s", vpcid)
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
		return nil, fmt.Errorf("failed to DescribeTransitGateways: %s", err.Error())
	}

	// Create Transit Gateway if it doesn't exist
	if len(exTg.TransitGateways) == 0 {
		Config.Logger.Printf("No transit gateway found. Creating one...")
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
			return nil, err
		}
		Config.Logger.Printf("Transit gateway created: %s", *tg.TransitGateway.TransitGatewayId)
		tgwAttachment, err := createTransitGatewayAttachments(ec2Local, vpcid, *tg.TransitGateway.TransitGatewayId, true, nil, userName)
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("Attachment created: %s", *tgwAttachment)
		_, err = TGWRoutes(userName, tg.TransitGateway.Options.AssociationDefaultRouteTableId, tgwAttachment, ec2Local, true, false, nil)
		if err != nil {
			return nil, err
		}
		resourceshare, err := shareTransitGateway(sess, *tg.TransitGateway.TransitGatewayArn, pm.AWSAccountId)
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("Resources shared: %s", *resourceshare)
		return tg.TransitGateway.TransitGatewayId, nil
	} else {
		tgwAttachment, err := createTransitGatewayAttachments(ec2Local, vpcid, *exTg.TransitGateways[len(exTg.TransitGateways)-1].TransitGatewayId, true, nil, userName)
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("Attachment created: %s", *tgwAttachment)
		resourceshare, err := shareTransitGateway(sess, *exTg.TransitGateways[len(exTg.TransitGateways)-1].TransitGatewayArn, pm.AWSAccountId)
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("Resources shared: %s", *resourceshare)
		return exTg.TransitGateways[len(exTg.TransitGateways)-1].TransitGatewayId, nil
	}
}

func createTransitGatewayAttachments(svc *ec2.EC2, vpcid string, tgwid string, local bool, sess *CREDS, userName string) (*string, error) {
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
		return nil, err
	}
	for *exTg.TransitGateways[0].State != "available" {
		Config.Logger.Printf("TransitGateway is in state: %s ...  Waiting for 5 seconds", *exTg.TransitGateways[0].State)
		// sleep for 2 sec
		time.Sleep(10 * time.Second)
		exTg, _ = svc.DescribeTransitGateways(tgInput)
	}
	networkInfo := &NetworkInfo{}
	if local {
		networkInfo, err = describeMainNetwork(vpcid, svc)
	} else {
		networkInfo, err = sess.describeWorkspaceNetwork(userName)
	}
	if err != nil {
		return nil, fmt.Errorf("Failed to get network info: %s", err)
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
		tgwAttachmentName := userToResourceName(userName, "service") + "tgwa"
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
		for i := range networkInfo.subnets.Subnets {
			tgwAttachmentInput.SubnetIds = append(tgwAttachmentInput.SubnetIds, networkInfo.subnets.Subnets[i].SubnetId)
		}
		tgwAttachment, err := svc.CreateTransitGatewayVpcAttachment(tgwAttachmentInput)
		if err != nil {
			return nil, fmt.Errorf("cannot create transitgatewayattachment: %s", err.Error())
		}
		return tgwAttachment.TransitGatewayVpcAttachment.TransitGatewayAttachmentId, nil
	} else {
		return exTgwAttachment.TransitGatewayAttachments[0].TransitGatewayAttachmentId, nil
	}
}

func deleteTransitGatewayAttachment(svc *ec2.EC2, tgwid string) (*string, error) {

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
		},
	}
	exTgwAttachment, err := svc.DescribeTransitGatewayAttachments(exTgwAttachmentInput)
	if err != nil {
		return nil, err
	}
	if len(exTgwAttachment.TransitGatewayAttachments) == 0 {
		return nil, fmt.Errorf("No transit gateway attachments found")
	}

	delTGWAttachmentInput := &ec2.DeleteTransitGatewayVpcAttachmentInput{
		TransitGatewayAttachmentId: exTgwAttachment.TransitGatewayAttachments[0].TransitGatewayAttachmentId,
	}
	delTGWAttachment, err := svc.DeleteTransitGatewayVpcAttachment(delTGWAttachmentInput)

	return delTGWAttachment.TransitGatewayVpcAttachment.TransitGatewayAttachmentId, nil
}

func shareTransitGateway(session *session.Session, tgwArn string, accountid string) (*string, error) {
	svc := ram.New(session)

	ramName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-ram"
	getResourceShareInput := &ram.GetResourceSharesInput{
		Name:                aws.String(ramName),
		ResourceOwner:       aws.String("SELF"),
		ResourceShareStatus: aws.String("ACTIVE"),
	}
	exRs, err := svc.GetResourceShares(getResourceShareInput)
	if err != nil {
		return nil, err
	}
	if len(exRs.ResourceShares) == 0 {
		Config.Logger.Printf("Did not find existing resource share, creating a resource share")
		resourceShareInput := &ram.CreateResourceShareInput{
			// Indicates whether principals outside your organization in Organizations can
			// be associated with a resource share.
			AllowExternalPrincipals: aws.Bool(true),
			Name:                    aws.String(ramName),
			Principals:              []*string{aws.String(accountid)},
			ResourceArns:            []*string{aws.String(tgwArn)},
			Tags: []*ram.Tag{
				{
					Key:   aws.String("Name"),
					Value: aws.String(ramName),
				},
				{
					Key:   aws.String("Environment"),
					Value: aws.String(os.Getenv("GEN3_ENDPOINT")),
				},
			},
		}
		resourceShare, err := svc.CreateResourceShare(resourceShareInput)
		if err != nil {
			return nil, err
		}
		return resourceShare.ResourceShare.ResourceShareArn, nil
	} else {
		listResourcesInput := &ram.ListResourcesInput{
			ResourceOwner: aws.String("SELF"),
			ResourceArns:  []*string{&tgwArn},
		}
		listResources, err := svc.ListResources(listResourcesInput)

		listPrincipalsInput := &ram.ListPrincipalsInput{
			ResourceArn:   aws.String(tgwArn),
			Principals:    []*string{aws.String(accountid)},
			ResourceOwner: aws.String("SELF"),
		}
		listPrincipals, err := svc.ListPrincipals(listPrincipalsInput)
		if err != nil {
			return nil, fmt.Errorf("failed to ListPrincipals: %s", err)
		}
		if len(listPrincipals.Principals) == 0 || len(listResources.Resources) == 0 {
			associateResourceShareInput := &ram.AssociateResourceShareInput{
				Principals:       []*string{aws.String(accountid)},
				ResourceArns:     []*string{&tgwArn},
				ResourceShareArn: exRs.ResourceShares[len(exRs.ResourceShares)-1].ResourceShareArn,
			}
			_, err := svc.AssociateResourceShare(associateResourceShareInput)
			if err != nil {
				return nil, err
			}
		} else {
			Config.Logger.Printf("TransitGateway is already shared with AWS account %s ", *listPrincipals.Principals[0].Id)
		}
		return exRs.ResourceShares[len(exRs.ResourceShares)-1].ResourceShareArn, nil
	}
}

func setupRemoteAccount(userName string, teardown bool) error {
	pm := Config.PayModelMap[userName]
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))
	svc := NewSession(sess, roleARN)

	ec2Local := ec2.New(sess)
	ec2Remote := ec2.New(session.New(&aws.Config{
		Credentials: svc.creds,
		Region:      aws.String("us-east-1"),
	}))

	vpcid := os.Getenv("GEN3_VPCID")
	Config.Logger.Printf("VPCID: %s", vpcid)
	err := svc.acceptTGWShare()
	if err != nil {
		return err
	}
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
		Config.Logger.Printf("Waiting to find ex_tgw")
		err := svc.acceptTGWShare()
		if err != nil {
			return err
		}
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
		tgw_attachment, err = deleteTransitGatewayAttachment(ec2Remote, *exTg.TransitGateways[0].TransitGatewayId)
		if err != nil {
			return err
		}
		Config.Logger.Printf("tgw_attachment: %s", *tgw_attachment)
	} else {
		tgw_attachment, err = createTransitGatewayAttachments(ec2Remote, *vpc.Vpcs[0].VpcId, *exTg.TransitGateways[0].TransitGatewayId, false, &svc, userName)
		if err != nil {
			return fmt.Errorf("Cannot create TransitGatewayAttachment: %s", err.Error())
		}
		Config.Logger.Printf("tgw_attachment: %s", *tgw_attachment)
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

func (creds *CREDS) acceptTGWShare() error {
	session := session.New(&aws.Config{
		Credentials: creds.creds,
		Region:      aws.String("us-east-1"),
	})
	svc := ram.New(session)

	resourceShareInvitation, err := svc.GetResourceShareInvitations(&ram.GetResourceShareInvitationsInput{})
	if err != nil {
		return err
	}

	if len(resourceShareInvitation.ResourceShareInvitations) == 0 {
		return nil
	} else {
		if *resourceShareInvitation.ResourceShareInvitations[0].Status != "ACCEPTED" {
			_, err := svc.AcceptResourceShareInvitation(&ram.AcceptResourceShareInvitationInput{
				ResourceShareInvitationArn: resourceShareInvitation.ResourceShareInvitations[0].ResourceShareInvitationArn,
			})
			if err != nil {
				return err
			}
			return nil
		}
		return nil
	}
}

func TGWRoutes(userName string, tgwRoutetableId *string, tgwAttachmentId *string, svc *ec2.EC2, local bool, teardown bool, sess *CREDS) (*string, error) {
	networkInfo := &NetworkInfo{}
	vpcid := os.Getenv("GEN3_VPCID")
	Config.Logger.Printf("VPCID: %s", vpcid)
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
			return nil, err
		}
		return delRouteInput.TransitGatewayRouteTableId, nil
	} else {
		for *tgwAttachment.TransitGatewayAttachments[0].State != "available" {
			Config.Logger.Printf("Transit Gateway Attachment is not ready. State is: %s", *tgwAttachment.TransitGatewayAttachments[0].State)
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
			return nil, err
		}

		if len(exRoutes.Routes) == 1 {
			delRouteInput := &ec2.DeleteTransitGatewayRouteInput{
				DestinationCidrBlock:       networkInfo.vpc.Vpcs[0].CidrBlock,
				TransitGatewayRouteTableId: tgwRoutetableId,
			}
			_, err := svc.DeleteTransitGatewayRoute(delRouteInput)
			if err != nil {
				return nil, err
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

		remoteRoute, err := ec2_remote.CreateRoute(remoteCreateRouteInput)
		if err != nil {
			return err
		}
		Config.Logger.Printf("Route added to remote VPC. %s", remoteRoute)

		localCreateRouteInput := &ec2.CreateRouteInput{
			DestinationCidrBlock: remote_network_info.vpc.Vpcs[0].CidrBlock,
			RouteTableId:         main_network_info.routeTable.RouteTables[0].RouteTableId,
			TransitGatewayId:     tgwId,
		}

		localRoute, err := ec2_local.CreateRoute(localCreateRouteInput)
		if err != nil {
			return err
		}
		Config.Logger.Printf("Route added to local VPC. %s", localRoute)
		return nil
	} else {
		remoteDeleteRouteInput := &ec2.DeleteRouteInput{
			DestinationCidrBlock: main_network_info.vpc.Vpcs[0].CidrBlock,
			RouteTableId:         remote_network_info.routeTable.RouteTables[0].RouteTableId,
		}
		_, err := ec2_remote.DeleteRoute(remoteDeleteRouteInput)
		if err != nil {
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
