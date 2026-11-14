package main

import (
	"context"
	"errors"
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

type runTicker interface {
	Tick(ctx context.Context) (service.RunStats, error)
}

var (
	fanout fanoutRunner
	runs   runTicker
)

const (
	runPhaseReserve = 150 * time.Second
	minFanoutBudget = 30 * time.Second
)

// fanoutContext bounds fan-out so the run phases keep time: its deadline is
// the invocation deadline minus 150 seconds, but never less than 30 seconds
// from now (and never after the invocation deadline).
func fanoutContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	budget := max(time.Until(deadline)-runPhaseReserve, minFanoutBudget)
	return context.WithTimeout(ctx, budget)
}

// handle runs fan-out (within its budget), then the run phases; both run even
// if the other fails, and their errors are joined. Logs carry counts only.
func handle(ctx context.Context, ev Event) error {
	switch ev.Kind {
	case "tick":
		fanCtx, cancel := fanoutContext(ctx)
		stats, fanErr := fanout.Run(fanCtx)
		cancel()
		log.Printf("customer updates tick: subs=%d delivered=%d rejected=%d skipped=%d",
			stats.Subs, stats.Delivered, stats.Rejected, stats.Skipped)
		var runErr error
		if runs != nil {
			var rs service.RunStats
			rs, runErr = runs.Tick(ctx)
			log.Printf("customer update runs: users=%d started=%d startFailed=%d startAmbiguous=%d finalized=%d expired=%d held=%d drafts=%d discarded=%d deferred=%d ignored=%d outOfScope=%d malformed=%d unreadable=%d oversized=%d leaked=%d servicesUnavailable=%d errors=%d itemErrors=%d",
				rs.Users, rs.Started, rs.StartFailed, rs.StartAmbiguous, rs.Finalized, rs.Expired, rs.Held, rs.Drafts, rs.Discarded, rs.Deferred,
				rs.Ignored, rs.OutOfScope, rs.Malformed, rs.Unreadable, rs.Oversized, rs.Leaked, rs.ServicesUnavailable, rs.Errors, rs.ItemErrors)
		}
		return errors.Join(fanErr, runErr)
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
	runs = newRuns(cfg, tableName, runsConfigFromEnv(os.Getenv))
	lambda.Start(handle)
}
