
openapi-hatchery: openapi-generator-cli.jar
	@rm -rf tmp
	@rm -rf hatchery/openapi
	@java -jar openapi-generator-cli.jar generate \
		-i openapis/openapi.yaml \
		-g go-server -o tmp --additional-properties sourceFolder=openapi
	@rm tmp/openapi/api_workspace_service.go
	@mv tmp/openapi hatchery/openapi

openapi-generator-cli.jar:
	@curl -o openapi-generator-cli.jar https://repo1.maven.org/maven2/org/openapitools/openapi-generator-cli/5.3.0/openapi-generator-cli-5.3.0.jar
