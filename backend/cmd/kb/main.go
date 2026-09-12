package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"sync"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagent"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type indexWorker interface {
	Enqueue(context.Context, model.IndexResource) error
	Tick(context.Context) (service.IndexTickResult, error)
}

var workerOnce sync.Once
var workerInstance indexWorker
var workerError error
var workerFactory = func(ctx context.Context) (indexWorker, error) {
	workerOnce.Do(func() { workerInstance, workerError = configuredWorker(ctx) })
	return workerInstance, workerError
}

func configuredWorker(ctx context.Context) (indexWorker, error) {
	values := map[string]string{}
	for _, name := range []string{"TABLE_NAME", "BUCKET_NAME", "KB_BUCKET_NAME", "KB_ID", "DATA_SOURCE_ID"} {
		values[name] = os.Getenv(name)
		if values[name] == "" {
			return nil, fmt.Errorf("required indexing configuration missing: %s", name)
		}
	}
	id := regexp.MustCompile(`^[A-Za-z0-9]{10}$`)
	if !id.MatchString(values["KB_ID"]) || !id.MatchString(values["DATA_SOURCE_ID"]) {
		return nil, fmt.Errorf("invalid knowledge-base/data-source identifiers")
	}
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithHTTPClient(awshttp.NewBuildableClient().WithTimeout(service.IndexOperationTimeout)))
	if err != nil {
		return nil, err
	}
	objects := s3.NewFromConfig(cfg)
	provider, err := service.NewIndexAWSProvider(objects, bedrockagent.NewFromConfig(cfg),
		values["BUCKET_NAME"], values["KB_BUCKET_NAME"], values["KB_ID"], values["DATA_SOURCE_ID"])
	if err != nil {
		return nil, err
	}
	repo := repository.NewDynamoDBRepository(dynamodb.NewFromConfig(cfg), values["TABLE_NAME"])
	return service.NewIndexingService(repo, provider, provider, values["BUCKET_NAME"]), nil
}

// DynamoDB retries only failed sequence numbers. Old images never drive exports:
// Enqueue records the canonical key and the scheduled worker rereads its source.
func handler(ctx context.Context, raw json.RawMessage) (interface{}, error) {
	var envelope struct {
		Records    json.RawMessage `json:"Records"`
		Source     string          `json:"source"`
		DetailType string          `json:"detail-type"`
		Action     string          `json:"action"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	stream := envelope.Records != nil
	tick := envelope.Action == "tick" || envelope.Action == "sync" ||
		(envelope.Source == "aws.events" && envelope.DetailType == "Scheduled Event")
	if !stream && !tick {
		return nil, fmt.Errorf("unsupported KB event; expected DynamoDB stream or scheduled tick")
	}
	worker, err := workerFactory(ctx)
	if err != nil {
		return nil, err
	}
	if stream {
		var event events.DynamoDBEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		response := events.DynamoDBEventResponse{BatchItemFailures: []events.DynamoDBBatchItemFailure{}}
		for _, record := range event.Records {
			if record.EventSource != "aws:dynamodb" {
				return nil, fmt.Errorf("unsupported stream source")
			}
			pk, sk := record.Change.Keys["PK"], record.Change.Keys["SK"]
			if pk.DataType() != events.DataTypeString || sk.DataType() != events.DataTypeString {
				return nil, fmt.Errorf("invalid canonical stream key")
			}
			key, relevant := model.CanonicalIndexResource(pk.String(), sk.String())
			if !relevant {
				continue
			}
			if err := worker.Enqueue(ctx, key); err != nil {
				if record.Change.SequenceNumber == "" {
					return nil, fmt.Errorf("stream sequence missing after enqueue failure: %w", err)
				}
				log.Printf("Index enqueue failed for %s: %v", key.Hash(), err)
				response.BatchItemFailures = append(response.BatchItemFailures,
					events.DynamoDBBatchItemFailure{ItemIdentifier: record.Change.SequenceNumber})
			}
		}
		return response, nil
	}
	result, err := worker.Tick(ctx)
	log.Printf("Index tick phase=%s resources=%d failed=%d providerJob=%s", result.Phase, result.Resources, result.Failed, result.ProviderJobID)
	return result, err
}

func main() { lambda.Start(handler) }
