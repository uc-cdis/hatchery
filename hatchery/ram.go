package hatchery

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ram"
)

func acceptTransitGatewayShare(pm *PayModel, sess *session.Session, ramArn *string) error {
	roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
	svc := NewSVC(sess, roleARN)

	// create RAM client in remote account.
	ramSvc := ram.New(session.Must(session.NewSession(&aws.Config{
		Credentials: svc.creds,
		Region:      aws.String("us-east-1"),
	})))

	// Check if the resource share is already accepted.
	// If not, accept the resource share
	ramName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-ram"
	exResourceShares, err := ramSvc.GetResourceShares(&ram.GetResourceSharesInput{
		Name:          aws.String(ramName),
		ResourceOwner: aws.String("OTHER-ACCOUNTS"),
	})
	if err != nil {
		// Log error
		Config.Logger.Print(err.Error())
		return err
	}
	if len(exResourceShares.ResourceShares) == 0 {
		roleARN := "arn:aws:iam::" + pm.AWSAccountId + ":role/csoc_adminvm"
		svc := NewSVC(sess, roleARN)
		err := svc.acceptTGWShare(ramArn)
		if err != nil {
			// Log error
			Config.Logger.Print(err.Error())
			return err
		}
	} else {
		// Log that resource share is already accepted
		Config.Logger.Printf("Resource share already accepted")
	}
	return nil
}

func (creds *CREDS) acceptTGWShare(ramArn *string) error {
	session := session.Must(session.NewSession(&aws.Config{
		Credentials: creds.creds,
		Region:      aws.String("us-east-1"),
	}))
	svc := ram.New(session)

	ramInvitationInput := &ram.GetResourceShareInvitationsInput{
		ResourceShareArns: []*string{
			ramArn,
		},
	}
	resourceShareInvitation, err := svc.GetResourceShareInvitations(ramInvitationInput)
	if err != nil {
		// Log error
		Config.Logger.Print(err.Error())
		return err
	}

	// Check if we have an invitation to accept
	if len(resourceShareInvitation.ResourceShareInvitations) == 0 {
		// No invitation found, possible that we have to wait a bit for the invitation to show up.
		Config.Logger.Printf("No resource share invitation found, waiting 10 seconds")
		time.Sleep(10 * time.Second)

		err := creds.acceptTGWShare(ramArn)
		if err != nil {
			return err
		}
		return nil
	} else {
		if *resourceShareInvitation.ResourceShareInvitations[0].Status != "ACCEPTED" {
			_, err := svc.AcceptResourceShareInvitation(&ram.AcceptResourceShareInvitationInput{
				ResourceShareInvitationArn: resourceShareInvitation.ResourceShareInvitations[0].ResourceShareInvitationArn,
			})
			if err != nil {
				return err
			}
			// Log that invitation was accepted
			Config.Logger.Printf("Resource share invitation accepted")
			return nil
		}
		// Log that invitation was already accepted
		Config.Logger.Printf("Resource share invitation already accepted")
		return nil
	}
}

func shareTransitGateway(session *session.Session, tgwArn string, accountid string) (*string, error) {
	// Share resources using resource share in Resource Access Manager
	// https://docs.aws.amazon.com/sdk-for-go/api/service/ram/#ResourceShare
	svc := ram.New(session)

	// RAM name cannot contain dots
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
		Config.Logger.Printf("Found existing resource share, associating resource share with account")
		listResourcesInput := &ram.ListResourcesInput{
			ResourceOwner: aws.String("SELF"),
			ResourceArns:  []*string{&tgwArn},
		}
		listResources, err := svc.ListResources(listResourcesInput)
		if err != nil {
			return nil, err
		}

		listPrincipalsInput := &ram.ListPrincipalsInput{
			ResourceArn:   aws.String(tgwArn),
			Principals:    []*string{aws.String(accountid)},
			ResourceOwner: aws.String("SELF"),
		}
		listPrincipals, err := svc.ListPrincipals(listPrincipalsInput)
		if err != nil {
			Config.Logger.Printf("failed to ListPrincipals: %s", listPrincipalsInput)
			return nil, fmt.Errorf("failed to ListPrincipals: %s", err)
		}
		if len(listPrincipals.Principals) == 0 || len(listResources.Resources) == 0 {
			Config.Logger.Printf("TransitGateway is not shared with AWS account %s, associating resource share with account", accountid)
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
