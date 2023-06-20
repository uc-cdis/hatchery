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

	Config.Logger.Printf("Setting up transit gateway in main account")
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

	Config.Logger.Printf("Setting up remote account ")
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

	// Get VPC CIDR blocks

	for _, cidr := range vpc.Vpcs[0].CidrBlockAssociationSet {
		// append to networkInfo struct
		networkInfo.vpcCidrBlocks = append(networkInfo.vpcCidrBlocks, cidr.CidrBlock)
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
			return nil, nil, nil, err
		}
		Config.Logger.Printf("Transit gateway created: %s", *tg.TransitGateway.TransitGatewayId)

		return tg.TransitGateway.TransitGatewayId, tg.TransitGateway.TransitGatewayArn, tg.TransitGateway.Options.AssociationDefaultRouteTableId, nil
	} else {
		Config.Logger.Print("Existing transit gateway found. Skipping creation...")
		return exTg.TransitGateways[len(exTg.TransitGateways)-1].TransitGatewayId, exTg.TransitGateways[len(exTg.TransitGateways)-1].TransitGatewayArn, exTg.TransitGateways[len(exTg.TransitGateways)-1].Options.AssociationDefaultRouteTableId, nil
	}
}

func createTransitGatewayAttachments(svc *ec2.EC2, vpcid string, tgwid string, local bool, sess *CREDS, userName string) (*string, error) {
	Config.Logger.Printf("Creating transit gateway attachment for VPC: %s", vpcid)
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
		Config.Logger.Printf("TransitGateway is in state: %s ...  Waiting for 10 seconds", *exTg.TransitGateways[0].State)
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
		Config.Logger.Printf("Local transitgateway attachment not found, creating new one")
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
		Config.Logger.Printf("Created transitgatewayattachment: %s", *tgwAttachment.TransitGatewayVpcAttachment.TransitGatewayAttachmentId)
		return tgwAttachment.TransitGatewayVpcAttachment.TransitGatewayAttachmentId, nil
	} else {
		Config.Logger.Printf("Local transitgateway attachment found, using existing one")
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
		Config.Logger.Printf("Error: %s", err.Error())
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
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
		Config.Logger.Printf("Waiting to find ex_tgw")
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
			return fmt.Errorf("Cannot create remote TransitGatewayAttachment: %s", err.Error())
		}
		Config.Logger.Printf("tgw_attachment: %s", *tgw_attachment)
	}

	// setup Transit Gateway Route Table
	err = TGWRoutes(userName, exTg.TransitGateways[0].Options.AssociationDefaultRouteTableId, tgw_attachment, ec2Local, false, teardown, &svc)
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

func TGWRoutes(userName string, tgwRoutetableId *string, tgwAttachmentId *string, svc *ec2.EC2, local bool, teardown bool, sess *CREDS) error {
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
		TransitGatewayAttachmentIds: []*string{tgwAttachmentId},
	}
	tgwAttachment, err := svc.DescribeTransitGatewayAttachments(tgwAttachmentInput)
	if err != nil {
		return fmt.Errorf("error DescribeTransitGatewayAttachments: %s", err.Error())
	}
	if teardown {
		//  delete routes to all VPC CIDR blocks
		for _, cidr := range networkInfo.vpcCidrBlocks {
			err := deleteTGWRoute(tgwRoutetableId, cidr, svc)
			if err != nil {
				return err
			}
		}

		return nil
	} else {
		for *tgwAttachment.TransitGatewayAttachments[0].State != "available" {
			Config.Logger.Printf("Transit Gateway Attachment is not ready. State is: %s", *tgwAttachment.TransitGatewayAttachments[0].State)
			tgwAttachment, err = svc.DescribeTransitGatewayAttachments(tgwAttachmentInput)
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

func deleteTGWRoute(routeTableId *string, cidrBlock *string, svc *ec2.EC2) error {
	Config.Logger.Printf("Deleting route to %s from route table %s", *cidrBlock, *routeTableId)
	delRouteInput := &ec2.DeleteTransitGatewayRouteInput{
		DestinationCidrBlock:       cidrBlock,
		TransitGatewayRouteTableId: routeTableId,
	}
	_, err := svc.DeleteTransitGatewayRoute(delRouteInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case "InvalidRoute.NotFound":
				// Route already deleted, we are happy :)
				return nil
			}
		}
		return err
	}
	return nil
}

func createTGWRoute(routeTableId *string, cidrBlock *string, attachmentId *string, local bool, svc *ec2.EC2) error {
	// Check if route already exists.
	// If it exists make sure it's not blackholed, and if it is delete it.
	// If it doesn't exist, create the route in the transit gateway	route table
	exRoutesInput := &ec2.SearchTransitGatewayRoutesInput{
		TransitGatewayRouteTableId: routeTableId,
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("route-search.subnet-of-match"),
				Values: []*string{cidrBlock},
			},
		},
	}
	exRoutes, err := svc.SearchTransitGatewayRoutes(exRoutesInput)
	if err != nil {
		// log error
		Config.Logger.Printf("error SearchTransitGatewayRoutes: %s", err.Error())
		return err
	}

	if len(exRoutes.Routes) == 1 {
		if local {
			// Delete route only if it's blackhole route
			if *exRoutes.Routes[0].State == "blackhole" {
				Config.Logger.Printf("Route is blackhole, deleting")
				delRouteInput := &ec2.DeleteTransitGatewayRouteInput{
					DestinationCidrBlock:       cidrBlock,
					TransitGatewayRouteTableId: routeTableId,
				}
				_, err := svc.DeleteTransitGatewayRoute(delRouteInput)
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
			_, err := svc.DeleteTransitGatewayRoute(delRouteInput)
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
	_, err = svc.CreateTransitGatewayRoute(tgRouteInput)
	if err != nil {
		return err
	}
	return nil
}
