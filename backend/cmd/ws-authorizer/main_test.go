package main

import (
	"context"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/ttobak/backend/internal/middleware"
)

func TestConnectRejectsDirectOriginEvenWithValidJWT(t *testing.T) {
	oldPool, oldVerifier := cognitoUserPoolID, verifyToken
	t.Cleanup(func() { cognitoUserPoolID, verifyToken = oldPool, oldVerifier })
	cognitoUserPoolID = "test-pool"
	verified := false
	verifyToken = func(string) (*middleware.ALBOIDCClaims, error) {
		verified = true
		return &middleware.ALBOIDCClaims{Sub: "synthetic-user"}, nil
	}
	event := events.APIGatewayCustomAuthorizerRequestTypeRequest{
		MethodArn:             "arn:aws:execute-api:ap-northeast-2:111111111111:api/production/$connect",
		QueryStringParameters: map[string]string{"token": "synthetic-valid-jwt"},
	}
	response, err := handler(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.PolicyDocument.Statement) != 1 || response.PolicyDocument.Statement[0].Effect != "Deny" {
		t.Fatalf("direct request with valid JWT was allowed: %+v", response.PolicyDocument)
	}
	if verified {
		t.Fatal("direct request reached JWT verification before the origin boundary")
	}
}
