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
	ResearchID  string `json:"researchId"`
	UserID      string `json:"userId"`
	Topic       string `json:"topic"`
	Mode        string `json:"mode"`                  // agent mode: plan/respond/execute/subpage
	QualityMode string `json:"qualityMode,omitempty"`  // quality: quick/standard/deep (for execute/subpage)
	S3Key       string `json:"s3Key"`
	ChatHistory string `json:"chatHistory,omitempty"`  // JSON string of messages (for respond)
	ParentID    string `json:"parentId,omitempty"`     // parent research ID (for subpage)
}

type ResearchResult struct {
	ResearchID string `json:"researchId"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

func handler(ctx context.Context, event ResearchEvent) (ResearchResult, error) {
	mode := event.Mode
	if mode == "" {
		mode = "execute" // backward compatibility
	}

	topicPreview := event.Topic
	if len(topicPreview) > 80 {
		topicPreview = topicPreview[:80]
	}
	log.Printf("Research worker: id=%s mode=%s quality=%s topic=%q", event.ResearchID, mode, event.QualityMode, topicPreview)

	switch mode {
	case "plan":
		return handleAgentMode(ctx, event, "plan")
	case "respond":
		return handleAgentMode(ctx, event, "respond")
	case "subpage":
		return handleAgentMode(ctx, event, "subpage")
	default: // "execute" and backward compat
		return handleAgentMode(ctx, event, "execute")
	}
}

func handleAgentMode(ctx context.Context, event ResearchEvent, agentMode string) (ResearchResult, error) {
	payload := map[string]string{
		"topic":      event.Topic,
		"agentMode":  agentMode,
		"researchId": event.ResearchID,
	}
	// Pass quality mode for execute/subpage (maps to quick/standard/deep)
	if event.QualityMode != "" {
		payload["mode"] = event.QualityMode
	}
	if event.ChatHistory != "" {
		payload["chatHistory"] = event.ChatHistory
	}
	if event.ParentID != "" {
		payload["parentId"] = event.ParentID
	}

	payloadBytes, _ := json.Marshal(payload)

	log.Printf("Invoking AgentCore %s (endpoint=%s) agentMode=%s", agentRuntimeId, endpointName, agentMode)

	sessionId := "ttobak-research-" + event.ResearchID
	if len(sessionId) > 40 {
		sessionId = sessionId[:40]
	}

	output, err := agentCoreClient.InvokeAgentRuntime(ctx, &bedrockagentcore.InvokeAgentRuntimeInput{
		AgentRuntimeArn:  aws.String(agentRuntimeId),
		Qualifier:        aws.String(endpointName),
		Payload:          payloadBytes,
		RuntimeSessionId: aws.String(sessionId),
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
			preview := respBody
			if len(preview) > 200 {
				preview = preview[:200]
			}
			log.Printf("AgentCore response (%d bytes): %s", len(respBody), preview)
		}
	}

	// For plan/respond: agent runs synchronously inside AgentCore (~10-30s).
	// For execute/subpage: agent runs in background (5-45 min), save_report writes result.
	var status string
	switch agentMode {
	case "plan":
		status = "planned"
	case "respond":
		status = "responded"
	default:
		status = "running"
	}

	log.Printf("Research %s mode=%s dispatched to AgentCore", event.ResearchID, agentMode)
	return ResearchResult{ResearchID: event.ResearchID, Status: status}, nil
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

func main() {
	lambda.Start(handler)
}
