package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
)

// fakeLambdaInvoker records the payload it was invoked with, or returns an
// error, without touching real AWS.
type fakeLambdaInvoker struct {
	lastPayload []byte
	invokeCount int
	err         error
}

func (f *fakeLambdaInvoker) Invoke(ctx context.Context, params *awslambda.InvokeInput, optFns ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error) {
	f.invokeCount++
	f.lastPayload = params.Payload
	if f.err != nil {
		return nil, f.err
	}
	return &awslambda.InvokeOutput{}, nil
}

// fakeAPIGWPoster records posted frames instead of hitting API Gateway.
type fakeAPIGWPoster struct {
	posts [][]byte
}

func (f *fakeAPIGWPoster) PostToConnection(ctx context.Context, params *apigatewaymanagementapi.PostToConnectionInput, optFns ...func(*apigatewaymanagementapi.Options)) (*apigatewaymanagementapi.PostToConnectionOutput, error) {
	f.posts = append(f.posts, params.Data)
	return &apigatewaymanagementapi.PostToConnectionOutput{}, nil
}

func TestExtractUserID(t *testing.T) {
	cases := []struct {
		name       string
		authorizer interface{}
		want       string
	}{
		{"userId key", map[string]interface{}{"userId": "u1"}, "u1"},
		{"falls back to principalId", map[string]interface{}{"principalId": "u2"}, "u2"},
		{"userId wins over principalId", map[string]interface{}{"userId": "u1", "principalId": "u2"}, "u1"},
		{"empty userId falls back to principalId", map[string]interface{}{"userId": "", "principalId": "u3"}, "u3"},
		{"nil authorizer", nil, ""},
		{"wrong type", "not-a-map", ""},
		{"empty map", map[string]interface{}{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractUserID(tc.authorizer)
			if got != tc.want {
				t.Fatalf("extractUserID(%v) = %q, want %q", tc.authorizer, got, tc.want)
			}
		})
	}
}

func TestBuildAskLivePayload_ServerSetUserID(t *testing.T) {
	msg := &wsMessage{
		Action:    "ask_live",
		Question:  "what's next?",
		Context:   "some transcript",
		MeetingID: "m1",
		SessionID: "s1",
	}
	payload := buildAskLivePayload(msg, "verified-user", "https://example.com/prod", "conn1")

	if payload["userId"] != "verified-user" {
		t.Fatalf("userId = %v, want verified-user", payload["userId"])
	}
	if payload["question"] != "what's next?" {
		t.Fatalf("question = %v", payload["question"])
	}
	if payload["meetingId"] != "m1" || payload["sessionId"] != "s1" {
		t.Fatalf("meetingId/sessionId not forwarded: %v", payload)
	}
	if payload["streamMode"] != "ask_live" {
		t.Fatalf("streamMode = %v, want ask_live", payload["streamMode"])
	}
	if payload["connectionId"] != "conn1" || payload["endpoint"] != "https://example.com/prod" {
		t.Fatalf("connectionId/endpoint not set: %v", payload)
	}
}

func TestBuildAskLivePayload_ClientCannotOverrideUserID(t *testing.T) {
	// A malicious client can't put its own "userId" field into wsMessage at
	// all (there's no such JSON field on the struct), but this locks in that
	// the payload's userId always comes from the function argument (the
	// server-verified identity), never from msg fields.
	msg := &wsMessage{Question: "q", SessionID: "victim-session"}
	payload := buildAskLivePayload(msg, "real-user", "https://x", "c1")
	if payload["userId"] != "real-user" {
		t.Fatalf("userId = %v, want real-user (server-set)", payload["userId"])
	}
}

func withFakeLambdaClient(t *testing.T, fake lambdaInvoker) {
	t.Helper()
	orig := lambdaClient
	lambdaClient = fake
	t.Cleanup(func() { lambdaClient = orig })
}

func baseAskLiveEvent(body string, authorizer interface{}) events.APIGatewayWebsocketProxyRequest {
	return events.APIGatewayWebsocketProxyRequest{
		Body: body,
		RequestContext: events.APIGatewayWebsocketProxyRequestContext{
			ConnectionID: "conn-1",
			DomainName:   "abc123.execute-api.ap-northeast-2.amazonaws.com",
			Stage:        "production",
			RouteKey:     "$default",
			Authorizer:   authorizer,
		},
	}
}

func TestHandleMessage_AskLive_MissingQuestion(t *testing.T) {
	fakeLambda := &fakeLambdaInvoker{}
	withFakeLambdaClient(t, fakeLambda)

	event := baseAskLiveEvent(`{"action":"ask_live","sessionId":"s1"}`, map[string]interface{}{"userId": "u1"})
	resp, err := handleMessage(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("StatusCode = %d, want 400", resp.StatusCode)
	}
	if fakeLambda.invokeCount != 0 {
		t.Fatalf("invokeCount = %d, want 0 (must not invoke QA lambda without a question)", fakeLambda.invokeCount)
	}
}

func TestHandleMessage_AskLive_InvokesWithServerUserID(t *testing.T) {
	fakeLambda := &fakeLambdaInvoker{}
	withFakeLambdaClient(t, fakeLambda)

	event := baseAskLiveEvent(
		`{"action":"ask_live","question":"hello","sessionId":"s1","meetingId":"m1"}`,
		map[string]interface{}{"userId": "authenticated-user"},
	)
	resp, err := handleMessage(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if fakeLambda.invokeCount != 1 {
		t.Fatalf("invokeCount = %d, want 1", fakeLambda.invokeCount)
	}

	var payload map[string]any
	if err := json.Unmarshal(fakeLambda.lastPayload, &payload); err != nil {
		t.Fatalf("failed to unmarshal invoke payload: %v", err)
	}
	if payload["userId"] != "authenticated-user" {
		t.Fatalf("payload userId = %v, want authenticated-user", payload["userId"])
	}
	if payload["question"] != "hello" {
		t.Fatalf("payload question = %v, want hello", payload["question"])
	}
}

func TestHandleMessage_AskLive_InvokeFailureSendsError(t *testing.T) {
	fakeLambda := &fakeLambdaInvoker{err: errors.New("boom")}
	withFakeLambdaClient(t, fakeLambda)

	event := baseAskLiveEvent(
		`{"action":"ask_live","question":"hello","sessionId":"s1"}`,
		map[string]interface{}{"userId": "u1"},
	)
	resp, err := handleMessage(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 500 {
		t.Fatalf("StatusCode = %d, want 500 on invoke failure", resp.StatusCode)
	}
}

func TestSendError_PostsErrorFrame(t *testing.T) {
	fake := &fakeAPIGWPoster{}
	sendError(context.Background(), fake, "conn-1", "boom")
	if len(fake.posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(fake.posts))
	}
	var resp wsResponse
	if err := json.Unmarshal(fake.posts[0], &resp); err != nil {
		t.Fatalf("failed to unmarshal posted frame: %v", err)
	}
	if resp.Type != "error" || resp.Error != "boom" {
		t.Fatalf("resp = %+v, want type=error error=boom", resp)
	}
}
