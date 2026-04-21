module github.com/ttobak/backend

go 1.24

require (
	github.com/aws/aws-lambda-go v1.53.0
	github.com/aws/aws-sdk-go-v2 v1.41.6
	github.com/aws/aws-sdk-go-v2/config v1.32.11
	github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue v1.20.34
	github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression v1.8.34
	github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi v1.29.14
	github.com/aws/aws-sdk-go-v2/service/bedrockagent v1.52.5
	github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime v1.51.5
	github.com/aws/aws-sdk-go-v2/service/bedrockruntime v1.50.1
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.56.1
	github.com/aws/aws-sdk-go-v2/service/eventbridge v1.45.22
	github.com/aws/aws-sdk-go-v2/service/kms v1.50.3
	github.com/aws/aws-sdk-go-v2/service/lambda v1.89.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.96.3
	github.com/aws/aws-sdk-go-v2/service/transcribe v1.54.2
	github.com/aws/aws-sdk-go-v2/service/translate v1.33.19
	github.com/awslabs/aws-lambda-go-api-proxy v0.16.2
	github.com/go-chi/chi/v5 v5.2.5
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/uuid v1.6.0
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.8 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.19.11 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.19 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.22 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.22 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.5 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.21 // indirect
	github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider v1.60.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/dynamodbstreams v1.32.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.11 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.11.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.0.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.30.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.35.16 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.41.8 // indirect
	github.com/aws/smithy-go v1.25.0 // indirect
)
