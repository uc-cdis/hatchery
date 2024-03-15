# Nextflow workspaces

When a container is configured with the `nextflow.enabled` flag enabled, Hatchery automatically creates the AWS resources required to run Nextflow workflows with the AWS Batch Nextflow executor.

Nextflow workspaces can be configured using the `nextflow` configuration block. See [here](/doc/howto/configuration.md) for details.

## Integration with pay models

It is recommended only to allow Nextflow workspaces when the user's pay model uses the user's own AWS account. This is a consequence of the following limitations:
- AWS Batch access cannot be controlled via resource tags. Because the `batch:DescribeJobs` and `batch:DescribeJobDefinitions` actions do not support a more granular authorization, we have to grant access to `*` which allows users to see all the jobs and job definitions in the account. This is not acceptable in a setting where users are sharing an AWS account.
- There is a limit of 50 job queues per AWS account. If we have more than 50 total Nextflow users this call will fail. An option would be to delete unused job queues (when users terminate their workspace), but we would still be limited to 50 concurrent Nextflow users in the same account.

## Automatically-created AWS resources

When a user **launches** a Nextflow workspace, Hatchery automatically creates the necessary resources in AWS if they do not already exist:
- VPC and Squid instance
- AWS Batch Compute environment
- S3 bucket for workflow inputs and intermediate files
- AWS Batch job queue
- IAM policy with access to `s3://<nextflow bucket>/<username>`
- IAM role with access to this policy ^
- IAM policy with access to `s3://<nextflow bucket>/<username>`, access to create batch jobs in the user’s job queue, and access to pass this role ^
- IAM user with access to this policy ^
- Access key for this user ^. Harchery then sets the key and secret as environment variables `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` in the user's workspace for use by Nextflow.

When a user **terminates** a Nextflow workspace, Hatchery automatically deletes resources in AWS:
- The user’s access key
- The Squid instance is stopped
- Note: The contents of `s3://<nextflow bucket>/<username>` are not deleted because researchers may need to keep the intermediary files. Instead of deleting, we could set bucket lifecycle rules to delete after X days.

## Hatchery access

To do the above, the service account used by Hatchery needs various permissions in Batch, IAM and S3. In cloud-automation deployments, these permissions are set in the [kube-setup-hatchery.sh](https://github.com/uc-cdis/cloud-automation/blob/master/gen3/bin/kube-setup-hatchery.sh) script.

### nextflow-global.imagebuilder-reader-role-arn

When the `nextflow.instance-ami-builder-arn` setting is used, Hatchery also needs access to list image builder images in the appropriate account. `kube-setup-hatchery.sh` assumes that a "nextflow-imagebuilder-reader" role already exists in the account (Acct1) that contains the image builder pipelines, and lets Hatchery (in Acct2) assume that role.

The role ARN is configured in Hatchery's `nextflow-global.imagebuilder-reader-role-arn` setting.

**"nextflow-imagebuilder-reader" role which should already exist:**

Permissions:
```
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "ListNextflowAmis",
            "Effect": "Allow",
            "Action": "imagebuilder:ListImagePipelineImages",
            "Resource": [
                "arn:aws:imagebuilder:us-east-1:<Acct1 ID>:image-pipeline/nextflow-fips",
                "arn:aws:imagebuilder:us-east-1:<Acct1 ID>:image-pipeline/nextflow-gpu-ami"
            ]
        }
    ]
}
```

Trust policy (allows Acct2):
```
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "AllowAssumingRole",
            "Effect": "Allow",
            "Principal": {
                "AWS": "arn:aws:iam::<Acct2 ID>:root"
            },
            "Action": "sts:AssumeRole"
        }
    ]
}
```

## Note about cloud-automation deployments

To enable the Nextflow feature in a Hatchery deployment created before version 2023.11/1.4.0, run `kubectl delete sa hatchery-service-account && gen3 kube-setup-hatchery` in order to recreate the Hatchery IAM role with additional access.
