package hatchery

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/efs"
)

type EFS struct {
	EFSArn        string
	FileSystemId  string
	AccessPointId string
}

func (creds *CREDS) getEFSFileSystem(userName string, svc *efs.EFS) (*efs.DescribeFileSystemsOutput, error) {
	fsName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + userToResourceName(userName, "pod") + "fs"
	input := &efs.DescribeFileSystemsInput{
		CreationToken: aws.String(fsName),
	}
	result, err := svc.DescribeFileSystems(input)
	if err != nil {
		return nil, fmt.Errorf("Failed to describe EFS FS: %s", err)
	}
	// return empty struct if no filesystems are found
	if len(result.FileSystems) == 0 {
		return nil, nil
	}
	return result, nil
}

// TODO: Check for MountTarget state regardless if fs exists or not
func (creds *CREDS) createMountTarget(FileSystemId string, svc *efs.EFS, userName string) (*efs.MountTargetDescription, error) {
	networkInfo, err := creds.describeWorkspaceNetwork(userName)
	if err != nil {
		return nil, err
	}
	input := &efs.CreateMountTargetInput{
		FileSystemId: aws.String(FileSystemId),
		SubnetId:     networkInfo.subnets.Subnets[0].SubnetId,
		// TODO: Make this correct, currently it's all using the same SG
		SecurityGroups: []*string{
			networkInfo.securityGroups.SecurityGroups[0].GroupId,
		},
	}

	result, err := svc.CreateMountTarget(input)
	if err != nil {
		return nil, fmt.Errorf("Failed to create mount target: %s", err)
	}
	return result, nil
}

func (creds *CREDS) createAccessPoint(FileSystemId string, userName string, svc *efs.EFS) (*string, error) {
	exAccessPointInput := &efs.DescribeAccessPointsInput{
		FileSystemId: &FileSystemId,
	}
	exResult, err := svc.DescribeAccessPoints(exAccessPointInput)
	if err != nil {
		return nil, err
	}

	if len(exResult.AccessPoints) == 0 {
		input := &efs.CreateAccessPointInput{
			ClientToken:  aws.String(fmt.Sprintf("ap-%s", userToResourceName(userName, "pod"))),
			FileSystemId: aws.String(FileSystemId),
			PosixUser: &efs.PosixUser{
				Gid: aws.Int64(100),
				Uid: aws.Int64(1000),
			},
			RootDirectory: &efs.RootDirectory{
				CreationInfo: &efs.CreationInfo{
					OwnerGid:    aws.Int64(100),
					OwnerUid:    aws.Int64(1000),
					Permissions: aws.String("0755"),
				},
				Path: aws.String("/"),
			},
		}

		result, err := svc.CreateAccessPoint(input)
		if err != nil {
			return nil, fmt.Errorf("Failed to create accessPoint: %s", err)
		}
		return result.AccessPointId, nil

	} else {
		return exResult.AccessPoints[0].AccessPointId, nil
	}

}

func (creds *CREDS) EFSFileSystem(userName string) (*EFS, error) {
	svc := efs.New(session.Must(session.NewSession(&aws.Config{
		Credentials: creds.creds,
		// TODO: Make this configurable
		Region: aws.String("us-east-1"),
	})))
	fsName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + userToResourceName(userName, "pod") + "fs"
	exisitingFS, err := creds.getEFSFileSystem(userName, svc)
	if err != nil {
		return nil, err
	}
	if exisitingFS == nil {
		input := &efs.CreateFileSystemInput{
			Backup:          aws.Bool(false),
			CreationToken:   aws.String(fsName),
			Encrypted:       aws.Bool(true),
			PerformanceMode: aws.String("generalPurpose"),
			Tags: []*efs.Tag{
				{
					Key:   aws.String("Name"),
					Value: aws.String(fsName),
				},
				{
					Key:   aws.String("Environment"),
					Value: aws.String(os.Getenv("GEN3_ENDPOINT")),
				},
			},
		}

		result, err := svc.CreateFileSystem(input)
		if err != nil {
			return nil, fmt.Errorf("Error creating EFS filesystem: %s", err)
		}

		exisitingFS, _ = creds.getEFSFileSystem(userName, svc)
		for *exisitingFS.FileSystems[0].LifeCycleState != "available" {
			Config.Logger.Printf("EFS filesystem is in state: %s ...  Waiting for 2 seconds", *exisitingFS.FileSystems[0].LifeCycleState)
			// sleep for 2 sec
			time.Sleep(2 * time.Second)
			exisitingFS, _ = creds.getEFSFileSystem(userName, svc)
		}

		// Create mount target
		mountTarget, err := creds.createMountTarget(*result.FileSystemId, svc, userName)
		if err != nil {
			return nil, fmt.Errorf("Failed to create EFS MountTarget: %s", err)
		}
		Config.Logger.Printf("MountTarget created: %s", *mountTarget.MountTargetId)
		accessPoint, err := creds.createAccessPoint(*result.FileSystemId, userName, svc)
		if err != nil {
			return nil, fmt.Errorf("Failed to create EFS AccessPoint: %s", err)
		}
		Config.Logger.Printf("AccessPoint created: %s", *accessPoint)

		return &EFS{
			EFSArn:        *result.FileSystemArn,
			FileSystemId:  *result.FileSystemId,
			AccessPointId: *accessPoint,
		}, nil
	} else {
		// create accesspoint if it doesn't exist
		accessPoint, err := svc.DescribeAccessPoints(&efs.DescribeAccessPointsInput{
			FileSystemId: exisitingFS.FileSystems[0].FileSystemId,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to describe accesspoint: %s", err)
		}
		var accessPointId string
		if len(accessPoint.AccessPoints) == 0 {
			accessPointResult, err := creds.createAccessPoint(*exisitingFS.FileSystems[0].FileSystemId, userName, svc)
			if err != nil {
				return nil, fmt.Errorf("failed to create EFS AccessPoint: %s", err)
			}
			accessPointId = *accessPointResult
		} else {
			accessPointId = *accessPoint.AccessPoints[0].AccessPointId
		}
		// create mountTarget if it doesn't exist
		exMountTarget, err := svc.DescribeMountTargets(&efs.DescribeMountTargetsInput{
			FileSystemId: exisitingFS.FileSystems[0].FileSystemId,
		})
		if err != nil {
			return nil, err
		}
		if len(exMountTarget.MountTargets) == 0 {
			mountTarget, err := creds.createMountTarget(*exisitingFS.FileSystems[0].FileSystemId, svc, userName)
			if err != nil {
				return nil, fmt.Errorf("Failed to create EFS MountTarget: %s", err)
			}
			Config.Logger.Printf("MountTarget created: %s", *mountTarget.MountTargetId)
		}

		return &EFS{
			EFSArn:        *exisitingFS.FileSystems[0].FileSystemArn,
			FileSystemId:  *exisitingFS.FileSystems[0].FileSystemId,
			AccessPointId: accessPointId,
		}, nil
	}
}
