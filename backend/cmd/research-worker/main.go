package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var (
	dynamoClient    *dynamodb.Client
	agentCoreClient *bedrockagentcore.Client
	tableName       string
	agentRuntimeId  string
	endpointName    string
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("unable to load SDK config: %v", err)
	}

	dynamoClient = dynamodb.NewFromConfig(cfg)
	agentCoreClient = bedrockagentcore.NewFromConfig(cfg)

	tableName = os.Getenv("TABLE_NAME")
	agentRuntimeId = os.Getenv("AGENTCORE_RUNTIME_ID")
	endpointName = os.Getenv("AGENTCORE_ENDPOINT_NAME")
	if endpointName == "" {
		endpointName = "DEFAULT"
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
	Error      string `json:"error,omitempty"`
}

func handler(ctx context.Context, event ResearchEvent) (ResearchResult, error) {
	log.Printf("Research worker: id=%s mode=%s topic=%q", event.ResearchID, event.Mode, event.Topic[:min(80, len(event.Topic))])

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
		RuntimeSessionId: aws.String("ttobak-research-" + event.ResearchID[:20]),
		ContentType:      aws.String("application/json"),
		Accept:           aws.String("application/json"),
	})
	if err != nil {
		log.Printf("AgentCore invoke failed: %v", err)
		updateStatus(ctx, event.ResearchID, "error", fmt.Sprintf("AgentCore invocation failed: %v", err))
		return ResearchResult{ResearchID: event.ResearchID, Status: "error", Error: err.Error()}, nil
	}

	var respBody string
	if output.Response != nil {
		defer output.Response.Close()
		b, err := io.ReadAll(output.Response)
		if err != nil {
			log.Printf("Response read error: %v", err)
		} else {
			respBody = string(b)
			log.Printf("AgentCore response (%d bytes): %s", len(respBody), respBody[:min(200, len(respBody))])
		}
	}

	// Agent runs in background inside AgentCore Runtime (5-45 min).
	// We only confirm it was kicked off successfully. save_report tool
	// writes the final result to DynamoDB when complete.
	log.Printf("Research %s dispatched to AgentCore", event.ResearchID)
	return ResearchResult{ResearchID: event.ResearchID, Status: "running"}, nil
}

func researchKey(researchID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: "RESEARCH#" + researchID},
		"SK": &types.AttributeValueMemberS{Value: "CONFIG"},
	}
}

func updateStatus(ctx context.Context, researchID, status, errMsg string) {
	update := expression.Set(expression.Name("status"), expression.Value(status)).
		Set(expression.Name("errorMessage"), expression.Value(errMsg))
	expr, _ := expression.NewBuilder().WithUpdate(update).Build()
	dynamoClient.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(tableName),
		Key:                       researchKey(researchID),
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func main() {
	lambda.Start(handler)
}
