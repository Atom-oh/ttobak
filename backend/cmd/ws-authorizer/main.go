package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/ttobak/backend/internal/middleware"
)

var (
	cognitoUserPoolID = os.Getenv("COGNITO_USER_POOL_ID")
)

func handler(ctx context.Context, event events.APIGatewayCustomAuthorizerRequestTypeRequest) (events.APIGatewayCustomAuthorizerResponse, error) {
	token := event.QueryStringParameters["token"]
	if token == "" {
		log.Println("ws-authorizer: no token in query string")
		return denyResponse(event.MethodArn), nil
	}

	if cognitoUserPoolID == "" {
		log.Println("ws-authorizer: COGNITO_USER_POOL_ID not set")
		return denyResponse(event.MethodArn), nil
	}

	claims, err := middleware.ParseVerifiedJWT(token)
	if err != nil {
		log.Printf("ws-authorizer: JWT verification failed: %v", err)
		return denyResponse(event.MethodArn), nil
	}

	if claims.Sub == "" {
		log.Println("ws-authorizer: empty sub claim")
		return denyResponse(event.MethodArn), nil
	}

	return allowResponse(claims.Sub, event.MethodArn), nil
}

func allowResponse(principalID, methodArn string) events.APIGatewayCustomAuthorizerResponse {
	return events.APIGatewayCustomAuthorizerResponse{
		PrincipalID: principalID,
		PolicyDocument: events.APIGatewayCustomAuthorizerPolicy{
			Version: "2012-10-17",
			Statement: []events.IAMPolicyStatement{
				{
					Action:   []string{"execute-api:Invoke"},
					Effect:   "Allow",
					Resource: []string{buildResourceArn(methodArn)},
				},
			},
		},
		Context: map[string]interface{}{
			"userId": principalID,
		},
	}
}

func denyResponse(methodArn string) events.APIGatewayCustomAuthorizerResponse {
	return events.APIGatewayCustomAuthorizerResponse{
		PrincipalID: "unauthorized",
		PolicyDocument: events.APIGatewayCustomAuthorizerPolicy{
			Version: "2012-10-17",
			Statement: []events.IAMPolicyStatement{
				{
					Action:   []string{"execute-api:Invoke"},
					Effect:   "Deny",
					Resource: []string{buildResourceArn(methodArn)},
				},
			},
		},
	}
}

// buildResourceArn converts a specific method ARN to a wildcard that covers all routes/stages.
// Input format:  arn:aws:execute-api:region:account:api-id/stage/method/resource
// Output format: arn:aws:execute-api:region:account:api-id/*
func buildResourceArn(methodArn string) string {
	// Allow all routes on this API once authenticated at $connect
	parts := splitN(methodArn, ":", 6)
	if len(parts) < 6 {
		return methodArn
	}
	apiParts := splitN(parts[5], "/", 2)
	return fmt.Sprintf("%s:%s:%s:%s:%s:%s/*", parts[0], parts[1], parts[2], parts[3], parts[4], apiParts[0])
}

func splitN(s, sep string, n int) []string {
	result := make([]string, 0, n)
	for i := 0; i < n-1; i++ {
		idx := indexOf(s, sep)
		if idx < 0 {
			break
		}
		result = append(result, s[:idx])
		s = s[idx+len(sep):]
	}
	result = append(result, s)
	return result
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func main() {
	lambda.Start(handler)
}
