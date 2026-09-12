package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type workerStub struct {
	keys  []model.IndexResource
	ticks int
}

func (s *workerStub) Enqueue(_ context.Context, key model.IndexResource) error {
	s.keys = append(s.keys, key)
	if key.ID == "fail" {
		return errors.New("synthetic queue failure")
	}
	return nil
}
func (s *workerStub) Tick(context.Context) (service.IndexTickResult, error) {
	s.ticks++
	return service.IndexTickResult{Phase: "RUNNING", ProviderJobID: "job"}, nil
}

func TestIndexStreamFiltersCanonicalKeysAndReturnsFailedSequence(t *testing.T) {
	stub := &workerStub{}
	prior := workerFactory
	workerFactory = func(context.Context) (indexWorker, error) { return stub, nil }
	t.Cleanup(func() { workerFactory = prior })
	event := events.DynamoDBEvent{}
	for _, test := range []struct{ pk, sk, sequence string }{
		{"USER#owner", "MEETING#m", "1"}, {"USER#owner", "DOC#d", "2"}, {"ACCOUNT#team", "DOC#fail", "3"},
		{"USER#owner", "SHAREDDOC#d", "4"}, {model.IndexJobsPK, "some-hash", "5"}, {"MEETING#m", "ATTACH#a", "6"},
	} {
		event.Records = append(event.Records, events.DynamoDBEventRecord{EventSource: "aws:dynamodb", EventName: "REMOVE",
			Change: events.DynamoDBStreamRecord{SequenceNumber: test.sequence, Keys: map[string]events.DynamoDBAttributeValue{
				"PK": events.NewStringAttribute(test.pk), "SK": events.NewStringAttribute(test.sk)}}})
	}
	raw, _ := json.Marshal(event)
	out, err := handler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	response := out.(events.DynamoDBEventResponse)
	if len(stub.keys) != 3 || len(response.BatchItemFailures) != 1 || response.BatchItemFailures[0].ItemIdentifier != "3" || stub.ticks != 0 {
		t.Fatalf("bad stream dispatch: %+v %+v", stub, response)
	}
}
func TestIndexWorkerDispatchNeverReturnsPlaceholderSuccess(t *testing.T) {
	stub := &workerStub{}
	prior := workerFactory
	workerFactory = func(context.Context) (indexWorker, error) { return stub, nil }
	t.Cleanup(func() { workerFactory = prior })
	for _, event := range []string{`{"action":"tick"}`, `{"action":"sync"}`, `{"source":"aws.events","detail-type":"Scheduled Event"}`} {
		value, err := handler(context.Background(), json.RawMessage(event))
		if err != nil {
			t.Fatal(err)
		}
		if value.(service.IndexTickResult).Phase != "RUNNING" {
			t.Fatal("submission was misreported")
		}
	}
	for _, event := range []string{`{"action":"query","query":"anything"}`, `{"action":"ingest"}`, `{}`, `{"body":"{\"action\":\"sync\"}"}`} {
		if _, err := handler(context.Background(), json.RawMessage(event)); err == nil {
			t.Fatalf("unsupported event succeeded: %s", event)
		}
	}
	if stub.ticks != 3 {
		t.Fatal("unsupported event triggered work")
	}
}
func TestIndexWorkerRequiresRuntimeConfiguration(t *testing.T) {
	t.Setenv("TABLE_NAME", "")
	if _, err := configuredWorker(context.Background()); err == nil {
		t.Fatal("missing configuration silently used production defaults")
	}
}
