package main

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var (
	dynamoClient  *dynamodb.Client
	s3Client      *s3.Client
	bedrockClient *bedrockagentruntime.Client
	tableName     string
	bucketName    string
	kbBucketName  string
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	dynamoClient = dynamodb.NewFromConfig(cfg)
	s3Client = s3.NewFromConfig(cfg)
	bedrockClient = bedrockagentruntime.NewFromConfig(cfg)

	tableName = os.Getenv("TABLE_NAME")
	if tableName == "" {
		tableName = "ttobak-main"
	}

	bucketName = os.Getenv("BUCKET_NAME")
	if bucketName == "" {
		bucketName = "ttobak-assets"
	}

	kbBucketName = os.Getenv("KB_BUCKET_NAME")
}

// KBRequest represents a Knowledge Base operation request
type KBRequest struct {
	Action string `json:"action"` // "sync", "query", "ingest"
	Query  string `json:"query,omitempty"`
	UserID string `json:"userId,omitempty"`
}

// KBResponse represents a Knowledge Base operation response
type KBResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

func handler(ctx context.Context, event events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	log.Printf("KB Lambda invoked: path=%s, method=%s", event.Path, event.HTTPMethod)

	var req KBRequest
	if err := json.Unmarshal([]byte(event.Body), &req); err != nil {
		return errorResponse(400, "Invalid request body")
	}

	switch req.Action {
	case "sync":
		return handleSync(ctx, req)
	case "query":
		return handleQuery(ctx, req)
	case "ingest":
		return handleIngest(ctx, req)
	default:
		return errorResponse(400, "Unknown action: "+req.Action)
	}
}

func handleSync(ctx context.Context, req KBRequest) (events.APIGatewayProxyResponse, error) {
	log.Printf("Syncing Knowledge Base")

	// TODO: Trigger Bedrock KB ingestion job
	// TODO: Sync meeting summaries to KB bucket

	return successResponse(KBResponse{
		Success: true,
		Message: "Knowledge Base sync initiated",
	})
}

func handleQuery(ctx context.Context, req KBRequest) (events.APIGatewayProxyResponse, error) {
	log.Printf("Querying Knowledge Base: %s", req.Query)

	if req.Query == "" {
		return errorResponse(400, "Query is required")
	}

	// TODO: Use Bedrock RetrieveAndGenerate API
	// TODO: Return relevant meeting information

	return successResponse(KBResponse{
		Success: true,
		Message: "Query completed",
		Data:    map[string]string{"result": "placeholder"},
	})
}

func handleIngest(ctx context.Context, req KBRequest) (events.APIGatewayProxyResponse, error) {
	log.Printf("Ingesting to Knowledge Base for user: %s", req.UserID)

	// TODO: Export user's meeting summaries to KB bucket
	// TODO: Start ingestion job

	return successResponse(KBResponse{
		Success: true,
		Message: "Ingestion started",
	})
}

func successResponse(resp KBResponse) (events.APIGatewayProxyResponse, error) {
	body, _ := json.Marshal(resp)
	return events.APIGatewayProxyResponse{
		StatusCode: 200,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Body: string(body),
	}, nil
}

func errorResponse(statusCode int, message string) (events.APIGatewayProxyResponse, error) {
	resp := KBResponse{
		Success: false,
		Error:   message,
	}
	body, _ := json.Marshal(resp)
	return events.APIGatewayProxyResponse{
		StatusCode: statusCode,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Body: string(body),
	}, nil
}

func main() {
	lambda.Start(handler)
}
