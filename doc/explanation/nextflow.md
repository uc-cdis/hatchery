# Nextflow workspaces

When a container is configured with the `enable-nextflow` flag enabled, Hatchery automatically creates the AWS resources required to run Nextflow workflows in AWS Batch.

## Global resources

At start-up, Hatchery checks if any of the configured containers has the `enable-nextflow` flag enabled. If so, the following resources are created automatically:
- AWS Batch Compute environment
- S3 bucket for workflow inputs and intermediate files

## Per-user resources

When a user **launches** a Nextflow workspace, Hatchery automatically creates the necessary resources in AWS if they do not already exist:
- AWS Batch job queue
- IAM policy with access to `s3://<nextflow bucket>/<username>`
- IAM role with access to this policy ^
- IAM policy with access to `s3://<nextflow bucket>/<username>`, access to create batch jobs in the user’s job queue, and access to pass this role ^
- IAM user with access to this policy ^
- Access key for this user ^. Harchery then sets the key and secret as environment variables `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` in the user's workspace for use by Nextflow.

When a user **terminates** a Nextflow workspace, Hatchery automatically deletes resources in AWS:
- The contents of `s3://<nextflow bucket>/<username>`
- The user’s access key

TODO finish this ^

## Hatchery access

To do the above, the service account used by Hatchery needs various permissions in Batch, IAM and S3. In cloud-automation deployments, these permissions are set in the `kube-setup-hatchery` script.
