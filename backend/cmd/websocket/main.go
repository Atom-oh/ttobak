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
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

var (
	repo             *repository.DynamoDBRepository
	novaSonicService *service.NovaSonicService
	tableName        string
	lambdaClient     *awslambda.Client
	qaFunctionName   string
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	// Initialize DynamoDB
	dynamoClient := dynamodb.NewFromConfig(cfg)
	tableName = os.Getenv("TABLE_NAME")
	if tableName == "" {
		tableName = "ttobak-main"
	}
	repo = repository.NewDynamoDBRepository(dynamoClient, tableName)

	// Initialize Bedrock for Nova Sonic
	bedrockClient := bedrockruntime.NewFromConfig(cfg)
	novaSonicService = service.NewNovaSonicService(bedrockClient)

	// Lambda client used to async-invoke the Python QA Lambda for streaming answers
	lambdaClient = awslambda.NewFromConfig(cfg)
	qaFunctionName = os.Getenv("QA_FUNCTION_NAME")
	if qaFunctionName == "" {
		qaFunctionName = "ttobak-qa"
	}
}

// WebSocketMessage represents an incoming WebSocket message
type WebSocketMessage struct {
	Action      string   `json:"action"`                // "start", "audio", "stop", "ask_live"
	MeetingID   string   `json:"meetingId,omitempty"`
	Language    string   `json:"language,omitempty"`    // Source language
	TargetLangs []string `json:"targetLangs,omitempty"` // Target languages for translation
	AudioData   string   `json:"audioData,omitempty"`   // Base64-encoded audio chunk
	SessionID   string   `json:"sessionId,omitempty"`
	Question    string   `json:"question,omitempty"`    // ask_live: user question
	Context     string   `json:"context,omitempty"`     // ask_live: transcript context
}

// WebSocketResponse represents an outgoing WebSocket message
type WebSocketResponse struct {
	Type         string            `json:"type"` // "transcript", "translation", "error", "session"
	Text         string            `json:"text,omitempty"`
	Language     string            `json:"language,omitempty"`
	IsFinal      bool              `json:"isFinal,omitempty"`
	SessionID    string            `json:"sessionId,omitempty"`
	Translations map[string]string `json:"translations,omitempty"`
	Error        string            `json:"error,omitempty"`
}

func handler(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	log.Printf("WebSocket event: routeKey=%s, connectionId=%s", event.RequestContext.RouteKey, event.RequestContext.ConnectionID)

	switch event.RequestContext.RouteKey {
	case "$connect":
		return handleConnect(ctx, event)
	case "$disconnect":
		return handleDisconnect(ctx, event)
	default:
		return handleMessage(ctx, event)
	}
}

func handleConnect(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	connectionID := event.RequestContext.ConnectionID
	log.Printf("New WebSocket connection: %s", connectionID)

	// Connection can store session state in DynamoDB if needed
	// JWT validation happens at API Gateway level via Lambda authorizer

	return events.APIGatewayProxyResponse{
		StatusCode: 200,
		Body:       "Connected",
	}, nil
}

func handleDisconnect(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	connectionID := event.RequestContext.ConnectionID
	log.Printf("WebSocket disconnection: %s", connectionID)

	// Cleanup session state from DynamoDB if needed

	return events.APIGatewayProxyResponse{
		StatusCode: 200,
		Body:       "Disconnected",
	}, nil
}

func handleMessage(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	connectionID := event.RequestContext.ConnectionID

	// Initialize API Gateway Management API client
	endpoint := fmt.Sprintf("https://%s/%s", event.RequestContext.DomainName, event.RequestContext.Stage)
	cfg, _ := config.LoadDefaultConfig(ctx)
	apiGwClient := apigatewaymanagementapi.NewFromConfig(cfg, func(o *apigatewaymanagementapi.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	// Parse message
	var msg WebSocketMessage
	if err := json.Unmarshal([]byte(event.Body), &msg); err != nil {
		sendError(ctx, apiGwClient, connectionID, "Invalid message format")
		return events.APIGatewayProxyResponse{StatusCode: 400}, nil
	}

	switch msg.Action {
	case "start":
		return handleStart(ctx, apiGwClient, connectionID, &msg)
	case "audio":
		return handleAudio(ctx, apiGwClient, connectionID, &msg)
	case "stop":
		return handleStop(ctx, apiGwClient, connectionID, &msg)
	case "ask_live":
		return handleAskLive(ctx, apiGwClient, connectionID, event, &msg)
	default:
		sendError(ctx, apiGwClient, connectionID, "Unknown action: "+msg.Action)
		return events.APIGatewayProxyResponse{StatusCode: 400}, nil
	}
}

func handleStart(ctx context.Context, apiGwClient *apigatewaymanagementapi.Client, connectionID string, msg *WebSocketMessage) (events.APIGatewayProxyResponse, error) {
	if msg.MeetingID == "" {
		sendError(ctx, apiGwClient, connectionID, "meetingId is required")
		return events.APIGatewayProxyResponse{StatusCode: 400}, nil
	}

	language := msg.Language
	if language == "" {
		language = "ko-KR" // Default to Korean
	}

	session, err := novaSonicService.StartSession(ctx, msg.MeetingID, language, msg.TargetLangs)
	if err != nil {
		sendError(ctx, apiGwClient, connectionID, "Failed to start session: "+err.Error())
		return events.APIGatewayProxyResponse{StatusCode: 500}, nil
	}

	// Send session info back to client
	response := WebSocketResponse{
		Type:      "session",
		SessionID: session.SessionID,
		Text:      "Session started",
	}
	sendMessage(ctx, apiGwClient, connectionID, &response)

	return events.APIGatewayProxyResponse{StatusCode: 200}, nil
}

func handleAudio(ctx context.Context, apiGwClient *apigatewaymanagementapi.Client, connectionID string, msg *WebSocketMessage) (events.APIGatewayProxyResponse, error) {
	if msg.SessionID == "" {
		sendError(ctx, apiGwClient, connectionID, "sessionId is required")
		return events.APIGatewayProxyResponse{StatusCode: 400}, nil
	}

	if msg.AudioData == "" {
		sendError(ctx, apiGwClient, connectionID, "audioData is required")
		return events.APIGatewayProxyResponse{StatusCode: 400}, nil
	}

	// Process audio chunk (placeholder - actual implementation needs bidirectional streaming)
	transcript, err := novaSonicService.ProcessAudioChunk(ctx, msg.SessionID, nil)
	if err != nil {
		sendError(ctx, apiGwClient, connectionID, "Failed to process audio: "+err.Error())
		return events.APIGatewayProxyResponse{StatusCode: 500}, nil
	}

	if transcript != "" {
		response := WebSocketResponse{
			Type:      "transcript",
			Text:      transcript,
			SessionID: msg.SessionID,
			IsFinal:   false,
		}
		sendMessage(ctx, apiGwClient, connectionID, &response)
	}

	return events.APIGatewayProxyResponse{StatusCode: 200}, nil
}

// handleAskLive forwards a live Q&A question to the Python QA Lambda.
// The QA Lambda streams answer tokens back over WebSocket via PostToConnection,
// so this handler only kicks off the async invocation and returns immediately.
func handleAskLive(ctx context.Context, apiGwClient *apigatewaymanagementapi.Client, connectionID string, event events.APIGatewayWebsocketProxyRequest, msg *WebSocketMessage) (events.APIGatewayProxyResponse, error) {
	question := msg.Question
	if question == "" {
		sendError(ctx, apiGwClient, connectionID, "question is required")
		return events.APIGatewayProxyResponse{StatusCode: 400}, nil
	}

	endpoint := fmt.Sprintf("https://%s/%s", event.RequestContext.DomainName, event.RequestContext.Stage)
	payload := map[string]any{
		"streamMode":   "ask_live",
		"connectionId": connectionID,
		"endpoint":     endpoint,
		"question":     question,
		"context":      msg.Context,
		"meetingId":    msg.MeetingID,
		"sessionId":    msg.SessionID,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		sendError(ctx, apiGwClient, connectionID, "Failed to encode ask_live payload")
		return events.APIGatewayProxyResponse{StatusCode: 500}, nil
	}

	_, err = lambdaClient.Invoke(ctx, &awslambda.InvokeInput{
		FunctionName:   aws.String(qaFunctionName),
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        payloadBytes,
	})
	if err != nil {
		log.Printf("ask_live invoke failed: %v", err)
		sendError(ctx, apiGwClient, connectionID, "Failed to start live Q&A")
		return events.APIGatewayProxyResponse{StatusCode: 500}, nil
	}

	return events.APIGatewayProxyResponse{StatusCode: 200}, nil
}

func handleStop(ctx context.Context, apiGwClient *apigatewaymanagementapi.Client, connectionID string, msg *WebSocketMessage) (events.APIGatewayProxyResponse, error) {
	if msg.SessionID == "" {
		sendError(ctx, apiGwClient, connectionID, "sessionId is required")
		return events.APIGatewayProxyResponse{StatusCode: 400}, nil
	}

	err := novaSonicService.StopSession(ctx, msg.SessionID)
	if err != nil {
		sendError(ctx, apiGwClient, connectionID, "Failed to stop session: "+err.Error())
		return events.APIGatewayProxyResponse{StatusCode: 500}, nil
	}

	response := WebSocketResponse{
		Type:      "session",
		SessionID: msg.SessionID,
		Text:      "Session ended",
	}
	sendMessage(ctx, apiGwClient, connectionID, &response)

	return events.APIGatewayProxyResponse{StatusCode: 200}, nil
}

func sendMessage(ctx context.Context, client *apigatewaymanagementapi.Client, connectionID string, response *WebSocketResponse) error {
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}

	_, err = client.PostToConnection(ctx, &apigatewaymanagementapi.PostToConnectionInput{
		ConnectionId: aws.String(connectionID),
		Data:         data,
	})
	return err
}

func sendError(ctx context.Context, client *apigatewaymanagementapi.Client, connectionID string, message string) {
	response := WebSocketResponse{
		Type:  "error",
		Error: message,
	}
	sendMessage(ctx, client, connectionID, &response)
}

func main() {
	lambda.Start(handler)
}
