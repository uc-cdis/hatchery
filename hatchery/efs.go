package hatchery

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
)

type EFS struct {
	EFSArn        string
	FileSystemId  string
	AccessPointId string
}

func (creds *CREDS) getEFSFileSystem(ctx context.Context, userName string, svc *efs.Client) (*efs.DescribeFileSystemsOutput, error) {
	fsName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + userToResourceName(userName, "pod") + "fs"
	fsName = truncateString(fsName, 64)
	input := &efs.DescribeFileSystemsInput{
		CreationToken: aws.String(fsName),
	}
	result, err := svc.DescribeFileSystems(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to describe EFS FS: %s", err)
	}
	// return empty struct if no filesystems are found
	if len(result.FileSystems) == 0 {
		return nil, nil
	}
	return result, nil
}

// TODO: Check for MountTarget state regardless if fs exists or not
func (creds *CREDS) createMountTarget(ctx context.Context, FileSystemId string, svc *efs.Client, userName string) (*efs.CreateMountTargetOutput, error) {
	networkInfo, err := creds.describeWorkspaceNetwork(userName)
	if err != nil {
		return nil, err
	}
	input := &efs.CreateMountTargetInput{
		FileSystemId: aws.String(FileSystemId),
		SubnetId:     networkInfo.subnets.Subnets[0].SubnetId,
		// TODO: Make this correct, currently it's all using the same SG
		SecurityGroups: []string{aws.ToString(networkInfo.securityGroups.SecurityGroups[0].GroupId)},
	}

	result, err := svc.CreateMountTarget(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("failed to create mount target: %s", err)
	}
	return result, nil
}

func (creds *CREDS) createAccessPoint(ctx context.Context, FileSystemId string, userName string, svc *efs.Client) (*string, error) {
	exAccessPointInput := &efs.DescribeAccessPointsInput{
		FileSystemId: &FileSystemId,
	}
	exResult, err := svc.DescribeAccessPoints(ctx, exAccessPointInput)
	if err != nil {
		return nil, err
	}
	ap := userToResourceName(userName, "service") + "-" + strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + "-accesspoint"
	ap = truncateString(ap, 64)
	if len(exResult.AccessPoints) == 0 {
		input := &efs.CreateAccessPointInput{
			// A string of up to 64 ASCII characters that Amazon EFS uses to ensure idempotent creation.
			ClientToken:  aws.String(ap),
			FileSystemId: aws.String(FileSystemId),
			PosixUser: &efstypes.PosixUser{
				Gid: aws.Int64(100),
				Uid: aws.Int64(1000),
			},
			RootDirectory: &efstypes.RootDirectory{
				CreationInfo: &efstypes.CreationInfo{
					OwnerGid:    aws.Int64(100),
					OwnerUid:    aws.Int64(1000),
					Permissions: aws.String("755"),
				},
				Path: aws.String("/pd"),
			},
		}

		result, err := svc.CreateAccessPoint(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("failed to create accessPoint: %s", err)
		}
		return result.AccessPointId, nil

	} else {
		return exResult.AccessPoints[0].AccessPointId, nil
	}

}

func (creds *CREDS) EFSFileSystem(userName string) (*EFS, error) {
	ctx := context.TODO()
	cfg := aws.Config{Region: "us-east-1", Credentials: creds.creds}
	svc := efs.NewFromConfig(cfg)

	fsName := strings.ReplaceAll(os.Getenv("GEN3_ENDPOINT"), ".", "-") + userToResourceName(userName, "pod") + "fs"
	fsName = truncateString(fsName, 64)
	exisitingFS, err := creds.getEFSFileSystem(ctx, userName, svc)
	if err != nil {
		return nil, err
	}
	if exisitingFS == nil {
		input := &efs.CreateFileSystemInput{
			Backup:          aws.Bool(false),
			CreationToken:   aws.String(fsName),
			Encrypted:       aws.Bool(true),
			PerformanceMode: efstypes.PerformanceModeGeneralPurpose,
			Tags: []efstypes.Tag{
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

		result, err := svc.CreateFileSystem(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("error creating EFS filesystem: %s", err)
		}

		exisitingFS, _ = creds.getEFSFileSystem(ctx, userName, svc)
		for exisitingFS.FileSystems[0].LifeCycleState != efstypes.LifeCycleStateAvailable {
			Config.Logger.Printf("EFS filesystem is in state: %s ...  Waiting for 2 seconds", exisitingFS.FileSystems[0].LifeCycleState)
			// sleep for 2 sec
			time.Sleep(2 * time.Second)
			exisitingFS, _ = creds.getEFSFileSystem(ctx, userName, svc)
		}

		// Create mount target
		mountTarget, err := creds.createMountTarget(ctx, *result.FileSystemId, svc, userName)
		if err != nil {
			return nil, fmt.Errorf("failed to create EFS MountTarget: %s", err)
		}
		Config.Logger.Printf("MountTarget created: %s", *mountTarget.MountTargetId)
		accessPoint, err := creds.createAccessPoint(ctx, *result.FileSystemId, userName, svc)
		if err != nil {
			return nil, fmt.Errorf("failed to create EFS AccessPoint: %s", err)
		}
		Config.Logger.Printf("AccessPoint created: %s", *accessPoint)

		return &EFS{
			EFSArn:        *result.FileSystemArn,
			FileSystemId:  *result.FileSystemId,
			AccessPointId: *accessPoint,
		}, nil
	} else {
		// create accesspoint if it doesn't exist
		accessPoint, err := svc.DescribeAccessPoints(ctx, &efs.DescribeAccessPointsInput{
			FileSystemId: exisitingFS.FileSystems[0].FileSystemId,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to describe accesspoint: %s", err)
		}
		var accessPointId string
		if len(accessPoint.AccessPoints) == 0 {
			accessPointResult, err := creds.createAccessPoint(ctx, *exisitingFS.FileSystems[0].FileSystemId, userName, svc)
			if err != nil {
				return nil, fmt.Errorf("failed to create EFS AccessPoint: %s", err)
			}
			accessPointId = *accessPointResult
		} else {
			accessPointId = *accessPoint.AccessPoints[0].AccessPointId
		}
		// create mountTarget if it doesn't exist
		exMountTarget, err := svc.DescribeMountTargets(ctx, &efs.DescribeMountTargetsInput{
			FileSystemId: exisitingFS.FileSystems[0].FileSystemId,
		})
		if err != nil {
			return nil, err
		}
		if len(exMountTarget.MountTargets) == 0 {
			mountTarget, err := creds.createMountTarget(ctx, *exisitingFS.FileSystems[0].FileSystemId, svc, userName)
			if err != nil {
				return nil, fmt.Errorf("failed to create EFS MountTarget: %s", err)
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
