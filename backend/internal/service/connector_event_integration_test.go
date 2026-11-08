package service_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/handler"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// eventWire fakes DynamoDB for the delegated event create path.
type eventWire struct {
	*noteWire
	account, member, noSealer bool
	contacts                  map[string]bool
	event, ref, receipt       map[string]any
	writes, batchGets         int
}

func (w *eventWire) Do(req *http.Request) (*http.Response, error) {
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		w.t.Fatal(err)
	}
	result, status := map[string]any{}, 200
	fail := func() {
		status = 400
		result = map[string]any{"__type": "TransactionCanceledException", "CancellationReasons": []any{map[string]any{"Code": "ConditionalCheckFailed"}}}
	}
	switch req.Header.Get("X-Amz-Target") {
	case "DynamoDB_20120810.TransactGetItems":
		result["Responses"] = []any{map[string]any{"Item": w.policy}, map[string]any{"Item": w.approval}}
	case "DynamoDB_20120810.GetItem":
		key := body["Key"].(map[string]any)
		pk, sk := key["PK"].(map[string]any)["S"].(string), key["SK"].(map[string]any)["S"].(string)
		switch {
		case pk == "ACCOUNT#acct" && sk == "META" && w.account:
			result["Item"] = map[string]any{"PK": noteString(pk), "SK": noteString(sk), "accountId": noteString("acct")}
		case pk == "ACCOUNT#acct" && sk == "MEMBER#user" && w.member:
			result["Item"] = map[string]any{"PK": noteString(pk), "SK": noteString(sk)}
		case pk == "USER#user" && sk == "CONNECTOR_MUTATION#client#request" && w.receipt != nil:
			result["Item"] = w.receipt
		case strings.HasPrefix(sk, "EVENTREF#") && w.ref != nil:
			result["Item"] = w.ref
		case strings.HasPrefix(sk, "EVENT#") && w.event != nil:
			result["Item"] = w.event
		}
	case "DynamoDB_20120810.BatchGetItem":
		w.batchGets++
		found := []any{}
		for _, spec := range body["RequestItems"].(map[string]any) {
			for _, k := range spec.(map[string]any)["Keys"].([]any) {
				id := strings.TrimPrefix(k.(map[string]any)["SK"].(map[string]any)["S"].(string), "CONTACT#")
				if w.contacts[id] {
					found = append(found, map[string]any{"contactId": noteString(id)})
				}
			}
		}
		result["Responses"] = map[string]any{"table": found}
	case "DynamoDB_20120810.Query":
		result["Items"] = []any{}
		if w.event != nil {
			result["Items"] = []any{w.event}
		}
	case "DynamoDB_20120810.TransactWriteItems":
		items := body["TransactItems"].([]any)
		for i, sk := range []string{"POLICY", "CONNECTOR_APPROVAL#client", "META", "MEMBER#user"} {
			check := items[i].(map[string]any)["ConditionCheck"].(map[string]any)
			if check["Key"].(map[string]any)["SK"].(map[string]any)["S"] != sk {
				w.t.Fatalf("missing atomic check %s", sk)
			}
		}
		if !w.account || !w.member {
			fail()
			break
		}
		switch len(items) {
		case 5: // replay-only probe with no receipt
			if w.receipt != nil {
				fail()
			}
		case 6: // replay: event and receipt unchanged
			if w.event == nil || w.receipt == nil || w.event["revision"].(map[string]any)["S"] != w.receipt["revision"].(map[string]any)["S"] {
				fail()
			}
		case 7:
			if w.receipt != nil {
				fail()
				break
			}
			w.writes++
			w.event = items[4].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
			w.ref = items[5].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
			w.receipt = items[6].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
			text := fmt.Sprint(w.event)
			for _, secret := range []string{"고객 리뷰", "홍길동", "a@example.com", "본사"} {
				if strings.Contains(text, secret) {
					w.t.Fatalf("event field %q written in plaintext", secret)
				}
			}
		default:
			w.t.Fatalf("unexpected transaction of %d items", len(items))
		}
	}
	raw, _ := json.Marshal(result)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(string(raw))), Request: req}, nil
}

func eventBridge(t *testing.T, w *eventWire) (*connector.Bridge, func(string, string, string, string) []byte) {
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", HTTPClient: w, Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
	})})
	repo := repository.NewDynamoDBRepository(client, "table")
	if !w.noSealer {
		repo.SetRecordSealer(eventTestSealer{})
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	spki, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	verifier, err := connector.NewVerifier(noteKeyARN, spki, "ttobak-api-bridge", repo.ConnectorGrantResolver(noteAudience, noteIssuer), nil)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(middleware.AuthWithConnectorGrants(nil))
	router.Get("/api/accounts/{accountId}/events", handler.NewAccountEventsHandler(service.NewAccountEventsService(repo)).List)
	bridge := &connector.Bridge{Verify: verifier.Verify, Router: router, EventMutate: handler.NewConnectorEventHandler(service.NewConnectorEventService(repo, noteAudience, noteIssuer)).Create}
	sign := func(method, path, body, operation string) []byte {
		args := []byte(fmt.Sprintf(`{"method":%q,"path":%q,"body":%s}`, method, path, body))
		hash := sha256.Sum256(args)
		now := time.Now().Unix()
		caps, generation := "\"read\"", 0
		if method == "POST" {
			caps, generation = "\"read\",\"write\"", 3
		}
		payload := []byte(fmt.Sprintf(`{"version":1,"protocol":"ttobak.connector","destination":"ttobak-api-bridge","sub":"user","clientId":"client","capabilities":[%s],"approvalGeneration":%d,"operation":%q,"argumentsSHA256":"%s","requestId":"request","issuedAt":%d,"expiresAt":%d,"upstreamExpiresAt":%d}`, caps, generation, operation, hex.EncodeToString(hash[:]), now, now+55, now+3600))
		digest, _ := connector.SigningDigest(payload)
		signature, e := rsa.SignPSS(rand.Reader, key, crypto.SHA256, digest, &rsa.PSSOptions{SaltLength: 32})
		if e != nil {
			t.Fatal(e)
		}
		envelope := []byte(fmt.Sprintf(`{"keyId":"%s","algorithm":"RSASSA_PSS_SHA_256","payload":"%s","signature":"%s"}`, noteKeyARN, connector.EncodeBridgeBytes(payload), connector.EncodeBridgeBytes(signature)))
		return connector.EncodeInvokeFrame(connector.InvokeFrame{Protocol: connector.InvokeProtocol, Operation: operation, RequestID: "request", Delegation: connector.EncodeBridgeBytes(envelope), Arguments: connector.EncodeBridgeBytes(args)})
	}
	return bridge, sign
}

// eventTestSealer reuses the reversible, binding-checked bridge sealer.
type eventTestSealer = bridgeTestSealer

const eventBody = `{"event":{"title":"고객 리뷰","startAt":"2026-11-10T14:00:00+09:00","location":"본사","attendees":[{"name":"홍길동","email":"a@example.com"}]},"idempotencyKey":"request"}`

func TestConnectorEventCreateReplayAndList(t *testing.T) {
	w := &eventWire{noteWire: newNoteWire(t), account: true, member: true}
	bridge, sign := eventBridge(t, w)
	call := func(path, body string) connector.InvokeResult {
		return bridge.Invoke(context.Background(), sign("POST", path, body, "api.accountEvents.create"))
	}
	first := call("/api/accounts/acct/events", eventBody)
	if first.StatusCode != 200 || w.writes != 1 {
		t.Fatalf("first=%+v writes=%d", first, w.writes)
	}
	id := w.event["eventId"].(map[string]any)["S"].(string)
	if w.event["SK"].(map[string]any)["S"] != "EVENT#2026-11-10T05:00:00Z#"+id || w.ref["eventSK"].(map[string]any)["S"] != w.event["SK"].(map[string]any)["S"] {
		t.Fatalf("event key: %v ref: %v", w.event["SK"], w.ref)
	}
	if out := call("/api/accounts/acct/events", eventBody); out.StatusCode != 200 || !strings.Contains(out.Body, id) || !strings.Contains(out.Body, `"replayed":true`) || w.writes != 1 {
		t.Fatalf("replay=%+v", out)
	}
	if out := call("/api/accounts/acct/events", strings.Replace(eventBody, "고객 리뷰", "다른 일정", 1)); out.StatusCode != 409 {
		t.Fatalf("changed arguments under the same key: %+v", out)
	}
	if out := call("/api/accounts/acct/events?replay=1", eventBody); out.StatusCode != 200 || !strings.Contains(out.Body, id) {
		t.Fatalf("probe=%+v", out)
	}
	list := bridge.Invoke(context.Background(), sign("GET", "/api/accounts/acct/events?limit=5", "null", "api.accountEvents.list"))
	if list.StatusCode != 200 || !strings.Contains(list.Body, id) || !strings.Contains(list.Body, "고객 리뷰") {
		t.Fatalf("list=%+v", list)
	}
}

func TestConnectorEventCreateFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*eventWire)
		body   string
		status int
		code   string
	}{
		"non-member":      {func(w *eventWire) { w.member = false }, eventBody, 403, "CAPABILITY_DENIED"},
		"no key":          {func(w *eventWire) { w.noSealer = true }, eventBody, 503, "EVENTS_UNAVAILABLE"},
		"unknown field":   {nil, strings.Replace(eventBody, `"location"`, `"createdBy":"x","location"`, 1), 400, "BAD_REQUEST"},
		"null field":      {nil, strings.Replace(eventBody, `"location":"본사"`, `"location":null`, 1), 400, "BAD_REQUEST"},
		"allDay type":     {nil, strings.Replace(eventBody, `"location"`, `"allDay":"yes","location"`, 1), 400, "BAD_REQUEST"},
		"bad start":       {nil, strings.Replace(eventBody, "2026-11-10T14:00:00+09:00", "tomorrow", 1), 400, "BAD_REQUEST"},
		"wrong key":       {nil, strings.Replace(eventBody, `"idempotencyKey":"request"`, `"idempotencyKey":"other"`, 1), 400, "BAD_REQUEST"},
		"unknown contact": {nil, strings.Replace(eventBody, `{"name":"홍길동"`, `{"contactId":"11111111-2222-4333-8444-555555555555","name":"홍길동"`, 1), 400, "BAD_REQUEST"},
		"replay missing":  {nil, "", 404, "RECEIPT_NOT_FOUND"},
	} {
		t.Run(name, func(t *testing.T) {
			w := &eventWire{noteWire: newNoteWire(t), account: true, member: true}
			if tc.mutate != nil {
				tc.mutate(w)
			}
			bridge, sign := eventBridge(t, w)
			path, body := "/api/accounts/acct/events", tc.body
			if name == "replay missing" {
				path, body = path+"?replay=1", eventBody
			}
			out := bridge.Invoke(context.Background(), sign("POST", path, body, "api.accountEvents.create"))
			if out.StatusCode != tc.status || !strings.Contains(out.Body, tc.code) || w.writes != 0 {
				t.Fatalf("%d %s writes=%d", out.StatusCode, out.Body, w.writes)
			}
		})
	}
}

func TestConnectorEventContactReferencesNeedMembershipFirst(t *testing.T) {
	body := strings.Replace(eventBody, `{"name":"홍길동"`, `{"contactId":"11111111-2222-4333-8444-555555555555","name":"홍길동"`, 1)
	w := &eventWire{noteWire: newNoteWire(t), account: true, member: false}
	bridge, sign := eventBridge(t, w)
	if out := bridge.Invoke(context.Background(), sign("POST", "/api/accounts/acct/events", body, "api.accountEvents.create")); out.StatusCode != 403 || w.batchGets != 0 {
		t.Fatalf("non-member probed contacts: %d batchGets=%d", out.StatusCode, w.batchGets)
	}
	w2 := &eventWire{noteWire: newNoteWire(t), account: true, member: true, contacts: map[string]bool{"11111111-2222-4333-8444-555555555555": true}}
	bridge2, sign2 := eventBridge(t, w2)
	if out := bridge2.Invoke(context.Background(), sign2("POST", "/api/accounts/acct/events", body, "api.accountEvents.create")); out.StatusCode != 200 || w2.writes != 1 {
		t.Fatalf("known contact: %+v", out)
	}
}

func TestConnectorEventReplaySurvivesADeletedAttendeeContact(t *testing.T) {
	const contact = "11111111-2222-4333-8444-555555555555"
	body := strings.Replace(eventBody, `{"name":"홍길동"`, `{"contactId":"`+contact+`","name":"홍길동"`, 1)
	w := &eventWire{noteWire: newNoteWire(t), account: true, member: true, contacts: map[string]bool{contact: true}}
	bridge, sign := eventBridge(t, w)
	call := func(path string) connector.InvokeResult {
		return bridge.Invoke(context.Background(), sign("POST", path, body, "api.accountEvents.create"))
	}
	if out := call("/api/accounts/acct/events"); out.StatusCode != 200 || w.writes != 1 {
		t.Fatalf("create: %+v", out)
	}
	id := w.event["eventId"].(map[string]any)["S"].(string)
	delete(w.contacts, contact) // a member deletes the referenced contact
	gets := w.batchGets
	for _, path := range []string{"/api/accounts/acct/events", "/api/accounts/acct/events?replay=1"} {
		if out := call(path); out.StatusCode != 200 || !strings.Contains(out.Body, id) || w.writes != 1 {
			t.Fatalf("%s after the contact was deleted: %+v", path, out)
		}
	}
	if w.batchGets != gets {
		t.Fatal("a replay must not re-check attendee contacts")
	}
}

func TestConnectorEventsStayReadableThroughTheBridge(t *testing.T) {
	w := &eventWire{noteWire: newNoteWire(t), account: true, member: true}
	bridge, sign := eventBridge(t, w)
	big := strings.Replace(eventBody, `"location":"본사"`, `"location":"본사","notes":"`+strings.Repeat("x", 8000)+`"`, 1)
	big = strings.Replace(big, `"title":"고객 리뷰"`, `"title":"`+strings.Repeat("t", 250)+`"`, 1)
	attendees := strings.Repeat(`{"name":"`+strings.Repeat("n", 200)+`"},`, 20)
	big = strings.Replace(big, `"attendees":[`, `"attendees":[`+attendees, 1)
	out := bridge.Invoke(context.Background(), sign("POST", "/api/accounts/acct/events", big, "api.accountEvents.create"))
	if out.StatusCode != 400 || w.writes != 0 {
		t.Fatalf("an event larger than a bridge read was created: %d writes=%d", out.StatusCode, w.writes)
	}
}
