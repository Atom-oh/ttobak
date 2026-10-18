package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/ttobak/backend/internal/middleware"
)

var (
	cognitoUserPoolID = os.Getenv("COGNITO_USER_POOL_ID")
	verifyToken       = middleware.ParseVerifiedJWT
	cloudFrontOrigin  *originVerifier
	// First-party app clients whose tokens may open a connection. The pool also
	// issues tokens to companion apps, which must not reach live QA. Empty denies all.
	allowedClients = parseClientList(os.Getenv("WS_ALLOWED_CLIENT_IDS"))
)

func parseClientList(v string) map[string]bool {
	out := map[string]bool{}
	for _, id := range strings.Split(v, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	return out
}

func handler(ctx context.Context, event events.APIGatewayCustomAuthorizerRequestTypeRequest) (events.APIGatewayCustomAuthorizerResponse, error) {
	token := event.QueryStringParameters["token"]
	if token == "" {
		log.Println("ws-authorizer: no token in query string")
		return denyResponse(event.MethodArn), nil
	}

	if result := cloudFrontOrigin.verify(ctx, event); !result.allowed() {
		log.Printf("ws-authorizer: origin verification rejected reason=%s category=%s", result.reason, result.category)
		return denyResponse(event.MethodArn), nil
	}

	if cognitoUserPoolID == "" {
		log.Println("ws-authorizer: COGNITO_USER_POOL_ID not set")
		return denyResponse(event.MethodArn), nil
	}

	claims, err := verifyToken(token)
	if err != nil {
		log.Println("ws-authorizer: JWT verification failed")
		return denyResponse(event.MethodArn), nil
	}

	if claims.Sub == "" {
		log.Println("ws-authorizer: empty sub claim")
		return denyResponse(event.MethodArn), nil
	}

	if !claims.IssuedToClient(allowedClients) {
		log.Println("ws-authorizer: token not issued to an allowed client")
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
					Resource: []string{methodArn},
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
					Resource: []string{methodArn},
				},
			},
		},
	}
}

func main() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatal("ws-authorizer: AWS configuration unavailable")
	}
	cloudFrontOrigin = newOriginVerifier(os.Getenv("WS_ORIGIN_SECRET_ARN"), secretsmanager.NewFromConfig(cfg))
	lambda.Start(handler)
}
