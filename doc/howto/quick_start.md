# Quick start for local development

- Create a configuration file at `./hatchery.json` with basic configuration:

```
{
    "user-namespace": "jupyter-pods",
    "sub-dir": "/lw-workspace",
    "user-volume-size": "10Gi"
}
```

- Install [nodemon](https://nodemon.io/)

- Run Hatchery:

`export GEN3_ENDPOINT=qa-heal.planx-pla.net; export GEN3_VPCID=qaplanetv1; nodemon --exec go run main.go -config ./hatchery.json --signal SIGTERM`

The API is exposed at http://0.0.0.0:8000.

# DynamoDB

- Set up DynamoDB locally. See AWS docs: https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.html
- Run local instance of DynamoDB, for example:

```
java -Djava.library.path=./Downloads/dynamodb_local_latest -jar ./Downloads/dynamodb_local_latest/DynamoDBLocal.jar -sharedDb
```

- Set `DYNAMODB_URL` environment variable for running tests against the database, or running the service locally
