
openapi-hatchery:
	@rm -rf tmp
	@rm -rf hatchery/openapi
	@java -jar openapi-generator-cli-5.2.1.jar generate \
		-i openapi/hatchery.yaml \
		-g go-server -o tmp --additional-properties sourceFolder=openapi
	@mv tmp/openapi hatchery/openapi

openapi-depends:
	@curl -O https://repo1.maven.org/maven2/org/openapitools/openapi-generator-cli/5.2.1/openapi-generator-cli-5.2.1.jar
