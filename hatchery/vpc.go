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

*/

type NetworkInfo struct {
	Vpc     ec2.DescribeVpcsOutput
	Subnets ec2.DescribeSubnetsOutput
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
	Config.Logger.Printf("Subnets: %s", subnets)
	network_info.Vpc = *vpc
	network_info.Subnets = *subnets
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
	if len(ex_tg.TransitGateways) <= 0 {
		Config.Logger.Printf("No transit gateway found. Creating one...")
		tg, err := ec2_local.CreateTransitGateway(&ec2.CreateTransitGatewayInput{
			DryRun:      aws.Bool(false),
			Description: aws.String("Transit gateway to connect external VPC's"),
			Options: &ec2.TransitGatewayRequestOptions{
				AutoAcceptSharedAttachments: aws.String("enable"),
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
		tgw_attachment, err := createLocalTransitGatewayAttachments(ec2_local, pm.VpcId, *ex_tg.TransitGateways[len(ex_tg.TransitGateways)-1].TransitGatewayId)
		if err != nil {
			return nil, err
		}
		Config.Logger.Printf("Attachment created: %s", *tgw_attachment)
		return tg.TransitGateway.TransitGatewayId, nil
	} else {
		Config.Logger.Printf("Existing TGW found: %s", *ex_tg.TransitGateways[len(ex_tg.TransitGateways)-1].TransitGatewayId)
		_, err := shareTransitGateway(sess, *ex_tg.TransitGateways[len(ex_tg.TransitGateways)-1].TransitGatewayArn, pm.AWSAccountId)
		if err != nil {
			return nil, err
		}
		return ex_tg.TransitGateways[len(ex_tg.TransitGateways)-1].TransitGatewayId, nil
	}
}

func createLocalTransitGatewayAttachments(svc *ec2.EC2, vpcid string, tgwid string) (*string, error) {
	// Check for existing transit gateway
	tg_input := &ec2.DescribeTransitGatewaysInput{
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
	network_info, err := describeMainNetwork(vpcid, svc)
	if err != nil {
		return nil, err
	}
	tgw_attachment_input := &ec2.CreateTransitGatewayVpcAttachmentInput{
		TransitGatewayId: ex_tg.TransitGateways[0].TransitGatewayId,
		VpcId:            network_info.Vpc.Vpcs[len(network_info.Vpc.Vpcs)-1].VpcId,
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
	for i := range network_info.Subnets.Subnets {
		tgw_attachment_input.SubnetIds = append(tgw_attachment_input.SubnetIds, network_info.Subnets.Subnets[i].SubnetId)
	}

	tgw_attachment, err := svc.CreateTransitGatewayVpcAttachment(tgw_attachment_input)
	if err != nil {
		return nil, err
	}
	return tgw_attachment.TransitGatewayVpcAttachment.TransitGatewayAttachmentId, nil
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
		listPrincipalsInput := &ram.ListPrincipalsInput{
			ResourceArn:   aws.String(tgwArn),
			Principals:    []*string{aws.String(accountid)},
			ResourceOwner: aws.String("SELF"),
		}
		list_principals, err := svc.ListPrincipals(listPrincipalsInput)
		if err != nil {
			return nil, fmt.Errorf("Failed to ListPrincipals: %s", err)
		}
		if len(list_principals.Principals) == 0 {
			Config.Logger.Printf("TODO: Add AWS account as principal to existing resource share here")
		} else {
			Config.Logger.Printf("TransitGateway is already shared with AWS account %s ", *list_principals.Principals[0].Id)
		}

		return ex_rs.ResourceShares[len(ex_rs.ResourceShares)-1].ResourceShareArn, nil
	}
}
