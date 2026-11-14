package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// Event is the Lambda input; Kind selects the work.
type Event struct {
	Kind string `json:"kind"`
}

type fanoutRunner interface {
	Run(ctx context.Context) (service.FanoutStats, error)
}

var fanout fanoutRunner

func handle(ctx context.Context, ev Event) error {
	switch ev.Kind {
	case "tick":
		stats, err := fanout.Run(ctx)
		log.Printf("customer updates tick: subs=%d delivered=%d rejected=%d skipped=%d",
			stats.Subs, stats.Delivered, stats.Rejected, stats.Skipped)
		return err
	default:
		return fmt.Errorf("unknown event kind %q", ev.Kind)
	}
}

func main() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("unable to load SDK config: %v", err)
	}
	tableName := os.Getenv("TABLE_NAME")
	if tableName == "" {
		log.Fatal("TABLE_NAME is required")
	}
	repo := repository.NewCustomerUpdateRepository(dynamodb.NewFromConfig(cfg), tableName)
	fanout = service.NewCustomerUpdateFanout(repo, time.Now)
	lambda.Start(handle)
}
