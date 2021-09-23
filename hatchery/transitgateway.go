package hatchery

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ram"
)

/*

# Main account
1. Create Transit Gateway if it doesn't exist
2. Share transit gateway with workspace account
3. Add main VPC to transit gateway if it isn't already

# workspace account
4. Accept shared transit gateway
5. Add default VPC to transit gateway
6. Add routes back to main VPC for the default route table.

# Main account
7. Add routes to worksapces via transit gateway to squid route table



main function order:
	Local account resources
	sharing and accepting.
	remote account resources
	setup routing

	Delete:
	teardown attachment
	delete route in squid route table

 TODO: MAKE SURE ALL THIS IS IDEMPOTENT!!! NEEDS TESTING!
*/

func setupTransitGateway(username string) error {
	_, err := createTransitGateway(username)
	if err != nil {
		return err
	}
	Config.Logger.Printf("Setting up remote account ")
	err = setupRemoteAccount(username)
	if err != nil {
		return err
	}
	return nil
}

func describeMainNetwork(vpcid string, svc *ec2.EC2) (*NetworkInfo, error) {
	network_info := NetworkInfo{}
	vpcInput := &ec2.DescribeVpcsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String(vpcid)},
			},
		},
	}
	vpc, _ := svc.DescribeVpcs(vpcInput)
	if len(vpc.Vpcs) == 0 {
		return nil, fmt.Errorf("No VPC's found: %s", vpc)
	}
	subnetInput := &ec2.DescribeSubnetsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []*string{aws.String(*vpc.Vpcs[0].VpcId)},
			},
			{
				Name:   aws.String("tag:kubernetes.io/role/internal-elb"),
				Values: []*string{aws.String("1")},
			},
		},
	}
	subnets, _ := svc.DescribeSubnets(subnetInput)
	network_info.vpc = vpc
	network_info.subnets = subnets
	return &network_info, nil
}

func createTransitGateway(userName string) (*string, error) {
	pm := Config.PayModelMap[userName]
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))

	// ec2 session to main AWS account.
	ec2_local := ec2.New(sess)

	// Check for existing transit gateway
	ex_tg, err := ec2_local.DescribeTransitGateways(&ec2.DescribeTransitGatewaysInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("state"),
				Values: []*string{aws.String("available"), aws.String("pending")},
			},
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String("MainTransitGateway")},
			},
		},
	})
	if err != nil {
		return nil, err
	}

	// Create Transit Gateway if it doesn't exist
	if len(ex_tg.TransitGateways) == 0 {
		Config.Logger.Printf("No transit gateway found. Creating one...")
		tg, err := ec2_local.CreateTransitGateway(&ec2.CreateTransitGatewayInput{
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
							Value: aws.String("MainTransitGateway"),
						},
					},
				},
			},
		})
		if err != nil {
			return nil, err
		}

		Config.Logger.Printf("Transit gateway created: %s", *tg.TransitGateway.TransitGatewayId)
		tgw_attachment, err := createTransitGatewayAttachments(ec2_local, pm.VpcId, *tg.TransitGateway.TransitGatewayId, true, nil)
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("Attachment created: %s", *tgw_attachment)
		resourceshare, err := shareTransitGateway(sess, *tg.TransitGateway.TransitGatewayArn, pm.AWSAccountId)
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("Resources shared: %s", *resourceshare)
		return tg.TransitGateway.TransitGatewayId, nil
	} else {
		tgw_attachment, err := createTransitGatewayAttachments(ec2_local, pm.VpcId, *ex_tg.TransitGateways[len(ex_tg.TransitGateways)-1].TransitGatewayId, true, nil)
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("Attachment created: %s", *tgw_attachment)
		resourceshare, err := shareTransitGateway(sess, *ex_tg.TransitGateways[len(ex_tg.TransitGateways)-1].TransitGatewayArn, pm.AWSAccountId)
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("Resources shared: %s", *resourceshare)
		return ex_tg.TransitGateways[len(ex_tg.TransitGateways)-1].TransitGatewayId, nil
	}
}

func createTransitGatewayAttachments(svc *ec2.EC2, vpcid string, tgwid string, local bool, sess *CREDS) (*string, error) {
	// Check for existing transit gateway
	tg_input := &ec2.DescribeTransitGatewaysInput{
		TransitGatewayIds: []*string{aws.String(tgwid)},
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("state"),
				Values: []*string{aws.String("available"), aws.String("pending")},
			},
		},
	}
	ex_tg, err := svc.DescribeTransitGateways(tg_input)
	if err != nil {
		return nil, err
	}
	for *ex_tg.TransitGateways[0].State != "available" {
		Config.Logger.Printf("TransitGateway is in state: %s ...  Waiting for 5 seconds", *ex_tg.TransitGateways[0].State)
		// sleep for 2 sec
		time.Sleep(10 * time.Second)
		ex_tg, _ = svc.DescribeTransitGateways(tg_input)
	}
	network_info := &NetworkInfo{}
	if local {
		network_info, err = describeMainNetwork(vpcid, svc)
	} else {
		network_info, err = sess.describeWorkspaceNetwork()
	}
	if err != nil {
		return nil, err
	}
	ex_tgw_attachment_input := &ec2.DescribeTransitGatewayAttachmentsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("resource-id"),
				Values: []*string{network_info.vpc.Vpcs[0].VpcId},
			},
			{
				Name:   aws.String("state"),
				Values: []*string{aws.String("available"), aws.String("pending")},
			},
		},
	}
	ex_tgw_attachment, err := svc.DescribeTransitGatewayAttachments(ex_tgw_attachment_input)
	if err != nil {
		return nil, err
	}
	if len(ex_tgw_attachment.TransitGatewayAttachments) == 0 {
		tgw_attachment_input := &ec2.CreateTransitGatewayVpcAttachmentInput{
			TransitGatewayId: ex_tg.TransitGateways[0].TransitGatewayId,
			VpcId:            network_info.vpc.Vpcs[len(network_info.vpc.Vpcs)-1].VpcId,
			TagSpecifications: []*ec2.TagSpecification{
				{
					ResourceType: aws.String("transit-gateway-attachment"),
					Tags: []*ec2.Tag{
						{
							Key:   aws.String("Name"),
							Value: aws.String("MainTGWAttachment"),
						},
					},
				},
			},
		}
		for i := range network_info.subnets.Subnets {
			tgw_attachment_input.SubnetIds = append(tgw_attachment_input.SubnetIds, network_info.subnets.Subnets[i].SubnetId)
		}

		tgw_attachment, err := svc.CreateTransitGatewayVpcAttachment(tgw_attachment_input)
		if err != nil {
			return nil, fmt.Errorf("cannot create transitgatewayattachment: %s", err.Error())
		}
		return tgw_attachment.TransitGatewayVpcAttachment.TransitGatewayAttachmentId, nil
	} else {
		return ex_tgw_attachment.TransitGatewayAttachments[0].TransitGatewayAttachmentId, nil
	}
}

func deleteTransitGatewayAttachment(userName string) error {
	pm := Config.PayModelMap[userName]
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

	ex_tgw_attachment_input := &ec2.DescribeTransitGatewayAttachmentsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String("MainTGWAttachment")},
			},
			{
				Name:   aws.String("state"),
				Values: []*string{aws.String("available"), aws.String("pending")},
			},
		},
	}
	ex_tgw_attachment, err := ec2_remote.DescribeTransitGatewayAttachments(ex_tgw_attachment_input)
	if err != nil {
		return err
	}
	if len(ex_tgw_attachment.TransitGatewayAttachments) == 0 {
		return fmt.Errorf("No transit gateway attachments found")
	}
	return nil
}

func shareTransitGateway(session *session.Session, tgwArn string, accountid string) (*string, error) {
	svc := ram.New(session)

	getResourceShareInput := &ram.GetResourceSharesInput{
		Name:                aws.String("MainTransitGatewayShare"),
		ResourceOwner:       aws.String("SELF"),
		ResourceShareStatus: aws.String("ACTIVE"),
	}
	ex_rs, err := svc.GetResourceShares(getResourceShareInput)
	if err != nil {
		return nil, err
	}
	if len(ex_rs.ResourceShares) == 0 {
		Config.Logger.Printf("Did not find existing resource share, creating a resource share")
		resourceShareInput := &ram.CreateResourceShareInput{
			// Indicates whether principals outside your organization in Organizations can
			// be associated with a resource share.
			AllowExternalPrincipals: aws.Bool(true),
			Name:                    aws.String("MainTransitGatewayShare"),
			Principals:              []*string{aws.String(accountid)},
			ResourceArns:            []*string{aws.String(tgwArn)},
		}
		resource_share, err := svc.CreateResourceShare(resourceShareInput)
		if err != nil {
			return nil, err
		}
		return resource_share.ResourceShare.ResourceShareArn, nil
	} else {
		listResourcesInput := &ram.ListResourcesInput{
			ResourceOwner: aws.String("SELF"),
			ResourceArns:  []*string{&tgwArn},
		}
		list_resources, err := svc.ListResources(listResourcesInput)

		listPrincipalsInput := &ram.ListPrincipalsInput{
			ResourceArn:   aws.String(tgwArn),
			Principals:    []*string{aws.String(accountid)},
			ResourceOwner: aws.String("SELF"),
		}
		list_principals, err := svc.ListPrincipals(listPrincipalsInput)
		if err != nil {
			return nil, fmt.Errorf("failed to ListPrincipals: %s", err)
		}
		if len(list_principals.Principals) == 0 || len(list_resources.Resources) == 0 {
			associateResourceShareInput := &ram.AssociateResourceShareInput{
				Principals:       []*string{aws.String(accountid)},
				ResourceArns:     []*string{&tgwArn},
				ResourceShareArn: ex_rs.ResourceShares[len(ex_rs.ResourceShares)-1].ResourceShareArn,
			}
			_, err := svc.AssociateResourceShare(associateResourceShareInput)
			if err != nil {
				return nil, err
			}
		} else {
			Config.Logger.Printf("TransitGateway is already shared with AWS account %s ", *list_principals.Principals[0].Id)
		}
		return ex_rs.ResourceShares[len(ex_rs.ResourceShares)-1].ResourceShareArn, nil
	}
}

func setupRemoteAccount(userName string) error {
	pm := Config.PayModelMap[userName]
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	sess := session.Must(session.NewSession(&aws.Config{
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	}))
	svc := NewSession(sess, roleARN)

	err := svc.acceptTGWShare()
	if err != nil {
		return err
	}

	ec2_remote := ec2.New(session.New(&aws.Config{
		Credentials: svc.creds,
		Region:      aws.String("us-east-1"),
	}))

	if err != nil {
		return err
	}
	exTgInput := &ec2.DescribeTransitGatewaysInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("state"),
				Values: []*string{aws.String("available"), aws.String("pending")},
			},
		},
	}
	ex_tg, err := ec2_remote.DescribeTransitGateways(exTgInput)
	if err != nil {
		return err
	}
	for len(ex_tg.TransitGateways) == 0 {
		Config.Logger.Printf("Waiting to find ex_tgw")
		err := svc.acceptTGWShare()
		if err != nil {
			return err
		}
		ex_tg, err = ec2_remote.DescribeTransitGateways(exTgInput)
		if err != nil {
			return err
		}
		time.Sleep(5 * time.Second)
	}
	network_info, err := svc.describeWorkspaceNetwork()
	if err != nil {
		return err
	}
	vpc := *network_info.vpc
	tgw_attachment, err := createTransitGatewayAttachments(ec2_remote, *vpc.Vpcs[0].VpcId, *ex_tg.TransitGateways[0].TransitGatewayId, false, &svc)
	if err != nil {
		return err
	}
	Config.Logger.Printf("tgw_attachment: %s", *tgw_attachment)
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
