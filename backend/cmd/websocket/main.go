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

// lambdaInvoker is the subset of *awslambda.Client used here — narrowed to
// an interface so tests can inject a fake instead of hitting real AWS.
type lambdaInvoker interface {
	Invoke(ctx context.Context, params *awslambda.InvokeInput, optFns ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error)
}

// apiGWPoster is the subset of *apigatewaymanagementapi.Client used here,
// narrowed the same way as lambdaInvoker.
type apiGWPoster interface {
	PostToConnection(ctx context.Context, params *apigatewaymanagementapi.PostToConnectionInput, optFns ...func(*apigatewaymanagementapi.Options)) (*apigatewaymanagementapi.PostToConnectionOutput, error)
}

var (
	lambdaClient   lambdaInvoker
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

	// Extract userId from Lambda authorizer context — this is the
	// server-verified identity (set by ws-authorizer from the Cognito JWT),
	// never trusted from the client-supplied message body.
	userID := extractUserID(event.RequestContext.Authorizer)

	payload := buildAskLivePayload(&msg, userID, endpoint, connID)
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

// extractUserID reads the userId (or principalId) set by ws-authorizer on
// the WebSocket $connect authorizer context. Returns "" if absent/malformed
// — callers must not proceed as an authenticated user in that case.
func extractUserID(authorizer interface{}) string {
	auth, ok := authorizer.(map[string]interface{})
	if !ok {
		return ""
	}
	if uid, ok := auth["userId"].(string); ok && uid != "" {
		return uid
	}
	if uid, ok := auth["principalId"].(string); ok {
		return uid
	}
	return ""
}

// buildAskLivePayload builds the async-invoke payload sent to the QA Lambda.
// userID always comes from extractUserID (the authorizer context), never
// from the client-supplied message, so the QA Lambda can trust it.
func buildAskLivePayload(msg *wsMessage, userID, endpoint, connID string) map[string]any {
	return map[string]any{
		"streamMode":   "ask_live",
		"connectionId": connID,
		"endpoint":     endpoint,
		"question":     msg.Question,
		"context":      msg.Context,
		"meetingId":    msg.MeetingID,
		"sessionId":    msg.SessionID,
		"userId":       userID,
	}
}

func sendError(ctx context.Context, client apiGWPoster, connID, message string) {
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
