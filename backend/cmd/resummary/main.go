package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

func run(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("resummary", flag.ContinueOnError)
	flags.SetOutput(output)
	meetingID := flags.String("meeting-id", "", "Exact meeting to inspect or recover")
	accountID := flags.String("expected-account", "", "Required AWS account guard")
	bucket := flags.String("bucket", "", "Configured application asset bucket")
	table := flags.String("table", "ttobak-main", "Application table")
	region := flags.String("region", "ap-northeast-2", "Application region")
	request := flags.Bool("request", false, "Queue one fresh saved-source summary through the application service")
	actionItems := flags.Bool("request-action-items", false, "Queue action extraction from the saved summary")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *meetingID == "" || *accountID == "" || *bucket == "" || flags.NArg() != 0 {
		return fmt.Errorf("meeting-id, expected-account and bucket are required")
	}
	if *request && *actionItems {
		return fmt.Errorf("request and request-action-items are mutually exclusive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(*region))
	if err != nil {
		return err
	}
	identity, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil || aws.ToString(identity.Account) != *accountID {
		return fmt.Errorf("AWS account verification failed")
	}
	repo := repository.NewDynamoDBRepositoryWithS3(dynamodb.NewFromConfig(cfg), *table, s3.NewFromConfig(cfg), *bucket).MetadataView()
	meeting, err := repo.GetMeetingByID(ctx, *meetingID)
	if err != nil {
		return err
	}
	if meeting == nil {
		return service.ErrNotFound
	}
	snapshot, err := repo.CaptureResummary(ctx, meeting.UserID, *meetingID, meeting.UserID)
	if err != nil {
		return err
	}
	if snapshot == nil {
		return service.ErrNotFound
	}
	state, err := repo.GetResummary(ctx, *meetingID)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(snapshot.Checks)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(encoded)
	sourceHash := hex.EncodeToString(hash[:])
	result := map[string]interface{}{"meetingId": *meetingID, "status": meeting.Status, "sourceHash": sourceHash, "sourceChecks": len(snapshot.Checks)}
	contentHash := sha256.Sum256([]byte(meeting.Content))
	result["summaryBytes"] = len(meeting.Content)
	if state != nil {
		result["analysis"] = map[string]interface{}{"runId": state.RunID, "status": state.Status, "errorCode": state.ErrorCode,
			"leaseUntil": state.LeaseUntil, "sourceMatchesPriorRun": state.SourceHash == sourceHash,
			"resultMatchesSavedSummary": state.ResultHash == hex.EncodeToString(contentHash[:])}
	}
	fields := map[string]interface{}{}
	var bindings []model.SummaryObject
	var sourceError error
	for _, field := range []string{"transcriptA", "transcriptB", "transcriptSegments"} {
		if *actionItems {
			break
		}
		value, _ := snapshot.Stored[field].(string)
		text, binding, readErr := repo.ReadResummaryTranscript(ctx, *meetingID, field, value)
		entry := map[string]interface{}{"bytes": len(text), "hasBinding": binding != nil}
		if readErr != nil {
			entry["error"] = readErr.Error()
			sourceError = errors.Join(sourceError, readErr)
		}
		if binding != nil {
			bindings = append(bindings, *binding)
		}
		fields[field] = entry
	}
	result["sources"] = fields
	if err := repo.CheckResummaryObjects(ctx, *meetingID, bindings); err != nil {
		result["objectCheckError"] = err.Error()
		sourceError = errors.Join(sourceError, err)
	}
	if *request {
		if sourceError != nil {
			return fmt.Errorf("source verification failed: %w", sourceError)
		}
		resummary := service.NewResummaryService(repo, service.NewMeetingService(repo), nil, nil, service.ResummaryPublisher(eventbridge.NewFromConfig(cfg)))
		queued, err := resummary.Request(ctx, meeting.UserID, *meetingID)
		if err != nil {
			return err
		}
		result["requested"] = queued
	}
	if *actionItems {
		analysis := service.NewMetadataActionItemsAnalysisService(repo, nil, service.ActionItemsPublisher(eventbridge.NewFromConfig(cfg)))
		queued, err := analysis.Request(ctx, meeting.UserID, *meetingID)
		if err != nil {
			return err
		}
		result["requestedActionItems"] = map[string]interface{}{"runId": queued.Analysis.RunID, "status": queued.Analysis.Status}
	}
	return json.NewEncoder(output).Encode(result)
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
