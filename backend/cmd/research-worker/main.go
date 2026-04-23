package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var (
	dynamoClient    *dynamodb.Client
	s3Client        *s3.Client
	agentCoreClient *bedrockagentcore.Client
	tableName       string
	kbBucketName    string
	agentRuntimeId  string
	endpointName    string
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("unable to load SDK config: %v", err)
	}

	dynamoClient = dynamodb.NewFromConfig(cfg)
	s3Client = s3.NewFromConfig(cfg)
	agentCoreClient = bedrockagentcore.NewFromConfig(cfg)

	tableName = os.Getenv("TABLE_NAME")
	kbBucketName = os.Getenv("KB_BUCKET_NAME")
	agentRuntimeId = os.Getenv("AGENTCORE_RUNTIME_ID")
	endpointName = os.Getenv("AGENTCORE_ENDPOINT_NAME")
	if endpointName == "" {
		endpointName = "ttobakResearchEndpoint"
	}
}

type ResearchEvent struct {
	ResearchID string `json:"researchId"`
	UserID     string `json:"userId"`
	Topic      string `json:"topic"`
	Mode       string `json:"mode"`
	S3Key      string `json:"s3Key"`
}

type ResearchResult struct {
	ResearchID string `json:"researchId"`
	Status     string `json:"status"`
	WordCount  int    `json:"wordCount,omitempty"`
	Error      string `json:"error,omitempty"`
}

func handler(ctx context.Context, event ResearchEvent) (ResearchResult, error) {
	log.Printf("Research worker: id=%s topic=%q mode=%s", event.ResearchID, event.Topic, event.Mode)

	payload := map[string]string{
		"topic":      event.Topic,
		"mode":       event.Mode,
		"researchId": event.ResearchID,
	}
	payloadBytes, _ := json.Marshal(payload)

	log.Printf("Invoking AgentCore %s (endpoint=%s)", agentRuntimeId, endpointName)

	output, err := agentCoreClient.InvokeAgentRuntime(ctx, &bedrockagentcore.InvokeAgentRuntimeInput{
		AgentRuntimeArn:  aws.String(agentRuntimeId),
		Qualifier:        aws.String(endpointName),
		Payload:          payloadBytes,
		RuntimeSessionId: aws.String("ttobak-research-" + event.ResearchID),
		ContentType:      aws.String("application/json"),
		Accept:           aws.String("application/json"),
	})
	if err != nil {
		updateStatus(ctx, event.ResearchID, "error", fmt.Sprintf("AgentCore invocation failed: %v", err))
		return ResearchResult{ResearchID: event.ResearchID, Status: "error", Error: err.Error()}, nil
	}

	var content string
	if output.Response != nil {
		defer output.Response.Close()
		respBody, err := io.ReadAll(output.Response)
		if err != nil {
			updateStatus(ctx, event.ResearchID, "error", fmt.Sprintf("Failed to read response: %v", err))
			return ResearchResult{ResearchID: event.ResearchID, Status: "error", Error: err.Error()}, nil
		}
		content = string(respBody)
		log.Printf("AgentCore response length: %d chars", len(content))
	}

	if content == "" {
		updateStatus(ctx, event.ResearchID, "error", "AgentCore returned empty response")
		return ResearchResult{ResearchID: event.ResearchID, Status: "error", Error: "empty response"}, nil
	}

	s3Key := event.S3Key
	if s3Key == "" {
		s3Key = fmt.Sprintf("shared/research/%s.md", event.ResearchID)
	}
	_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(kbBucketName),
		Key:         aws.String(s3Key),
		Body:        strings.NewReader(content),
		ContentType: aws.String("text/markdown; charset=utf-8"),
	})
	if err != nil {
		updateStatus(ctx, event.ResearchID, "error", fmt.Sprintf("S3 write failed: %v", err))
		return ResearchResult{ResearchID: event.ResearchID, Status: "error", Error: err.Error()}, nil
	}

	wordCount := len(strings.Fields(content))
	updateComplete(ctx, event.ResearchID, wordCount)

	log.Printf("Research %s complete: %d words", event.ResearchID, wordCount)
	return ResearchResult{ResearchID: event.ResearchID, Status: "done", WordCount: wordCount}, nil
}

func updateStatus(ctx context.Context, researchID, status, errMsg string) {
	_, err := dynamoClient.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "RESEARCH#" + researchID},
			"SK": &types.AttributeValueMemberS{Value: "CONFIG"},
		},
		UpdateExpression: aws.String("SET #s = :s, errorMessage = :e"),
		ExpressionAttributeNames: map[string]string{
			"#s": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":s": &types.AttributeValueMemberS{Value: status},
			":e": &types.AttributeValueMemberS{Value: errMsg},
		},
	})
	if err != nil {
		log.Printf("Failed to update research %s status: %v", researchID, err)
	}
}

func updateComplete(ctx context.Context, researchID string, wordCount int) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := dynamoClient.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName),
		Key: map[string]types.AttributeValue{
			"PK": &types.AttributeValueMemberS{Value: "RESEARCH#" + researchID},
			"SK": &types.AttributeValueMemberS{Value: "CONFIG"},
		},
		UpdateExpression: aws.String("SET #s = :s, completedAt = :c, wordCount = :w"),
		ExpressionAttributeNames: map[string]string{
			"#s": "status",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":s": &types.AttributeValueMemberS{Value: "done"},
			":c": &types.AttributeValueMemberS{Value: now},
			":w": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", wordCount)},
		},
	})
	if err != nil {
		log.Printf("Failed to update research %s completion: %v", researchID, err)
	}
}

func main() {
	lambda.Start(handler)
}
