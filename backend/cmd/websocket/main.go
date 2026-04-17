package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
)

var (
	lambdaClient   *awslambda.Client
	qaFunctionName string
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}
	lambdaClient = awslambda.NewFromConfig(cfg)
	qaFunctionName = os.Getenv("QA_FUNCTION_NAME")
	if qaFunctionName == "" {
		qaFunctionName = "ttobak-qa"
	}
}

type wsMessage struct {
	Action    string `json:"action"`
	Question  string `json:"question,omitempty"`
	Context   string `json:"context,omitempty"`
	MeetingID string `json:"meetingId,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

type wsResponse struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

func handler(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	switch event.RequestContext.RouteKey {
	case "$connect":
		log.Printf("WebSocket connected: %s", event.RequestContext.ConnectionID)
		return events.APIGatewayProxyResponse{StatusCode: 200, Body: "Connected"}, nil
	case "$disconnect":
		log.Printf("WebSocket disconnected: %s", event.RequestContext.ConnectionID)
		return events.APIGatewayProxyResponse{StatusCode: 200, Body: "Disconnected"}, nil
	default:
		return handleMessage(ctx, event)
	}
}

func handleMessage(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	connID := event.RequestContext.ConnectionID
	endpoint := fmt.Sprintf("https://%s/%s", event.RequestContext.DomainName, event.RequestContext.Stage)

	cfg, _ := config.LoadDefaultConfig(ctx)
	apigwClient := apigatewaymanagementapi.NewFromConfig(cfg, func(o *apigatewaymanagementapi.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	var msg wsMessage
	if err := json.Unmarshal([]byte(event.Body), &msg); err != nil {
		sendError(ctx, apigwClient, connID, "Invalid message format")
		return events.APIGatewayProxyResponse{StatusCode: 400}, nil
	}

	if msg.Action != "ask_live" {
		sendError(ctx, apigwClient, connID, "Unknown action: "+msg.Action)
		return events.APIGatewayProxyResponse{StatusCode: 400}, nil
	}

	if msg.Question == "" {
		sendError(ctx, apigwClient, connID, "question is required")
		return events.APIGatewayProxyResponse{StatusCode: 400}, nil
	}

	// Extract userId from Lambda authorizer context
	userID := ""
	if auth, ok := event.RequestContext.Authorizer.(map[string]interface{}); ok {
		if uid, ok := auth["userId"].(string); ok {
			userID = uid
		} else if uid, ok := auth["principalId"].(string); ok {
			userID = uid
		}
	}

	payload := map[string]any{
		"streamMode":   "ask_live",
		"connectionId": connID,
		"endpoint":     endpoint,
		"question":     msg.Question,
		"context":      msg.Context,
		"meetingId":    msg.MeetingID,
		"sessionId":    msg.SessionID,
		"userId":       userID,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		sendError(ctx, apigwClient, connID, "Failed to encode payload")
		return events.APIGatewayProxyResponse{StatusCode: 500}, nil
	}

	_, err = lambdaClient.Invoke(ctx, &awslambda.InvokeInput{
		FunctionName:   aws.String(qaFunctionName),
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        payloadBytes,
	})
	if err != nil {
		log.Printf("ask_live invoke failed: %v", err)
		sendError(ctx, apigwClient, connID, "Failed to start live Q&A")
		return events.APIGatewayProxyResponse{StatusCode: 500}, nil
	}

	return events.APIGatewayProxyResponse{StatusCode: 200}, nil
}

func sendError(ctx context.Context, client *apigatewaymanagementapi.Client, connID, message string) {
	resp := wsResponse{Type: "error", Error: message}
	data, _ := json.Marshal(resp)
	client.PostToConnection(ctx, &apigatewaymanagementapi.PostToConnectionInput{
		ConnectionId: aws.String(connID),
		Data:         data,
	})
}

func main() {
	lambda.Start(handler)
}
