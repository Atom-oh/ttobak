package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

const testRunID = "0123456789abcdef0123456789abcdef"

type fakeAgentRuntime struct {
	in     *bedrockagentcore.InvokeAgentRuntimeInput
	out    *bedrockagentcore.InvokeAgentRuntimeOutput
	err    error
	closed bool
}

type closeTracker struct {
	io.Reader
	f *fakeAgentRuntime
}

func (c closeTracker) Close() error { c.f.closed = true; return nil }

func (f *fakeAgentRuntime) InvokeAgentRuntime(_ context.Context, in *bedrockagentcore.InvokeAgentRuntimeInput, _ ...func(*bedrockagentcore.Options)) (*bedrockagentcore.InvokeAgentRuntimeOutput, error) {
	f.in = in
	return f.out, f.err
}

func runtimeReturning(status int32, body string) *fakeAgentRuntime {
	f := &fakeAgentRuntime{}
	f.out = &bedrockagentcore.InvokeAgentRuntimeOutput{StatusCode: aws.Int32(status), Response: closeTracker{strings.NewReader(body), f}}
	return f
}

func TestAgentInvoker_Accepted(t *testing.T) {
	f := runtimeReturning(200, `{"accepted":true,"runId":"`+testRunID+`"}`)
	inv := &agentInvoker{client: f, arn: "arn:aws:bedrock-agentcore:us-east-1:1:runtime/x", qualifier: "DEFAULT"}
	if err := inv.Invoke(context.Background(), testRunID, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	sid := aws.ToString(f.in.RuntimeSessionId)
	if sid != "ttobak-cu-"+testRunID || len(sid) < 33 || aws.ToString(f.in.Qualifier) != "DEFAULT" || !f.closed {
		t.Fatalf("session=%q qualifier=%q closed=%v", sid, aws.ToString(f.in.Qualifier), f.closed)
	}
}

func TestAgentInvoker_NotAccepted(t *testing.T) {
	// kind: "contract" = definite payload rejection, "refused" = other definite
	// refusal, "unknown" = the run may be running.
	cases := []struct {
		name string
		f    *fakeAgentRuntime
		kind string
		code string
	}{
		{"contract code", runtimeReturning(400, `{"error":{"code":"item_url","message":"x"}}`), "contract", "item_url"},
		{"too large body", runtimeReturning(413, `{"error":{"code":"TOO_LARGE"}}`), "contract", "TOO_LARGE"},
		{"no status, contract code", &fakeAgentRuntime{out: &bedrockagentcore.InvokeAgentRuntimeOutput{
			Response: io.NopCloser(strings.NewReader(`{"error":{"code":"shape"}}`))}}, "contract", "shape"},
		{"busy", runtimeReturning(409, `{"error":{"code":"BUSY","message":"x"}}`), "refused", "BUSY"},
		{"unavailable", runtimeReturning(503, `{"error":{"code":"UNAVAILABLE"}}`), "refused", "UNAVAILABLE"},
		{"bad code", runtimeReturning(503, `{"error":{"code":"<script>"}}`), "refused", "error"},
		{"status", runtimeReturning(500, `{"accepted":true,"runId":"`+testRunID+`"}`), "refused", "status_500"},
		{"run exists", runtimeReturning(409, `{"error":{"code":"RUN_EXISTS"}}`), "refused", "RUN_EXISTS"},
		{"other run", runtimeReturning(200, `{"accepted":true,"runId":"ffffffffffffffffffffffffffffffff"}`), "unknown", ""},
		{"not json", runtimeReturning(200, `nope`), "unknown", ""},
		{"too large", runtimeReturning(200, strings.Repeat(" ", maxInvokeResponseBytes+1)), "unknown", ""},
		{"no body", &fakeAgentRuntime{out: &bedrockagentcore.InvokeAgentRuntimeOutput{}}, "unknown", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := (&agentInvoker{client: c.f}).Invoke(context.Background(), testRunID, nil)
			var na *service.RunNotAcceptedError
			switch c.kind {
			case "unknown":
				if !errors.Is(err, service.ErrRunOutcomeUnknown) || errors.As(err, &na) {
					t.Fatalf("err = %v, want outcome unknown", err)
				}
			default:
				if !errors.As(err, &na) || na.Code != c.code || na.Contract != (c.kind == "contract") || !errors.Is(err, service.ErrRunNotAccepted) {
					t.Fatalf("err = %#v, want %s %s", err, c.kind, c.code)
				}
			}
		})
	}
	transport := errors.New("transport")
	if err := (&agentInvoker{client: &fakeAgentRuntime{err: transport}}).Invoke(context.Background(), testRunID, nil); !errors.Is(err, transport) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunsConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	c := runsConfigFromEnv(env(nil))
	if ok, warn := c.runsEnabled(); ok || warn != "" || c.EndpointName != "DEFAULT" {
		t.Fatalf("unset: %+v", c)
	}
	c = runsConfigFromEnv(env(map[string]string{"CUSTOMER_UPDATE_RUNTIME_ARN": "arn:x"}))
	if ok, warn := c.runsEnabled(); ok || warn == "" {
		t.Fatal("runtime without KMS key must stay off with a warning")
	}
	c = runsConfigFromEnv(env(map[string]string{"CUSTOMER_UPDATE_RUNTIME_ARN": "arn:x", "KMS_KEY_ID": "k", "CUSTOMER_UPDATE_ENDPOINT_NAME": "prod"}))
	if ok, _ := c.runsEnabled(); !ok || c.EndpointName != "prod" {
		t.Fatalf("enabled: %+v", c)
	}
	for raw, want := range map[string]int{"": 30, "abc": 30, "-1": 30, "0": 0, "12": 12, " 7 ": 7} {
		c = runsConfigFromEnv(env(map[string]string{"CUSTOMER_UPDATE_RESEARCH_PAIRS_PER_DAY": raw}))
		if c.ResearchPairsPerDay != want {
			t.Fatalf("research cap %q = %d, want %d", raw, c.ResearchPairsPerDay, want)
		}
	}
}

type fakeMembers struct{ err error }

func (f fakeMembers) RequireDirectMember(context.Context, string, string) error { return f.err }

type fakeProfiles struct {
	members  []model.AccountMember
	profiles map[string]*model.User
	gets     int
}

func (f *fakeProfiles) ListAccountMembers(context.Context, string) ([]model.AccountMember, error) {
	return f.members, nil
}
func (f *fakeProfiles) GetUserByID(_ context.Context, id string) (*model.User, error) {
	f.gets++
	return f.profiles[id], nil
}

func member(acct, user string) model.AccountMember {
	return model.AccountMember{PK: model.PrefixAccount + acct, SK: model.PrefixMember + user, AccountID: acct, UserID: user}
}

func TestAccountMemberNames(t *testing.T) {
	repo := &fakeProfiles{
		members: []model.AccountMember{member("a1", "u1"), member("a1", "u2"), member("a1", "u3"), member("a2", "u4"),
			{PK: model.PrefixAccount + "a1", SK: model.PrefixMember + "zz", AccountID: "a1", UserID: "u5"}},
		profiles: map[string]*model.User{
			"u1": {UserID: "u1", Name: " 김민수 "}, "u2": {UserID: "u2"}, "u4": {UserID: "u4", Name: "Other"}, "u5": {UserID: "u5", Name: "Forged"},
		},
	}
	names, err := (&accountMemberNames{members: fakeMembers{}, repo: repo}).AccountMemberNames(context.Background(), "u1", "a1")
	if err != nil || len(names) != 1 || names[0] != "김민수" {
		t.Fatalf("names=%v err=%v", names, err)
	}
	denied := errors.New("forbidden")
	if _, err := (&accountMemberNames{members: fakeMembers{err: denied}, repo: repo}).AccountMemberNames(context.Background(), "u9", "a1"); !errors.Is(err, denied) {
		t.Fatalf("err = %v", err)
	}
}

func TestAccountMemberNamesCap(t *testing.T) {
	repo := &fakeProfiles{profiles: map[string]*model.User{}}
	for i := 0; i < maxMemberNames+40; i++ {
		id := fmt.Sprintf("u%04d", i)
		repo.members = append(repo.members, member("a1", id))
		if i%2 == 0 || i >= 40 { // the first 40 members alternate between named and unnamed
			repo.profiles[id] = &model.User{UserID: id, Name: "Name " + id}
		}
	}
	names, err := (&accountMemberNames{members: fakeMembers{}, repo: repo}).AccountMemberNames(context.Background(), "u1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != maxMemberNames || maxMemberNames != 500 {
		t.Fatalf("names = %d, want 500", len(names))
	}
	if repo.gets != maxMemberNames+20 {
		t.Fatalf("profile reads = %d, want reading past unnamed members up to 500 names", repo.gets)
	}
}

func TestAgentCoreOptionsDisableRetries(t *testing.T) {
	var o bedrockagentcore.Options
	agentCoreOptions(&o)
	if _, ok := o.Retryer.(aws.NopRetryer); !ok || o.Retryer.MaxAttempts() != 1 || o.RetryMaxAttempts != 1 {
		t.Fatalf("retryer = %T attempts = %d", o.Retryer, o.RetryMaxAttempts)
	}
}

type deadlineRuntime struct {
	fakeAgentRuntime
	left time.Duration
	ok   bool
}

func (d *deadlineRuntime) InvokeAgentRuntime(ctx context.Context, in *bedrockagentcore.InvokeAgentRuntimeInput, fns ...func(*bedrockagentcore.Options)) (*bedrockagentcore.InvokeAgentRuntimeOutput, error) {
	dl, ok := ctx.Deadline()
	d.left, d.ok = time.Until(dl), ok
	return nil, errors.New("stop")
}

func TestAgentInvokerTimeout(t *testing.T) {
	d := &deadlineRuntime{}
	_ = (&agentInvoker{client: d}).Invoke(context.Background(), testRunID, nil)
	if !d.ok || d.left > invokeTimeout || d.left < invokeTimeout-5*time.Second {
		t.Fatalf("deadline set=%v left=%v", d.ok, d.left)
	}
}
