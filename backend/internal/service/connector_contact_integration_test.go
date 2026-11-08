package service_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
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
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/handler"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/sealing"
	"github.com/ttobak/backend/internal/service"
)

type contactWire struct {
	*noteWire
	contact, contactReceipt                       map[string]any
	member, account                               bool
	writes                                        int
	missingChecked                                bool
	lose, revokeAfterWrite, revokeOnQuery, reject bool
	noSealer                                      bool
}

func (w *contactWire) Do(req *http.Request) (*http.Response, error) {
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		w.t.Fatal(err)
	}
	target := req.Header.Get("X-Amz-Target")
	result := map[string]any{}
	status := 200
	switch target {
	case "DynamoDB_20120810.TransactGetItems":
		result["Responses"] = []any{map[string]any{"Item": w.policy}, map[string]any{"Item": w.approval}}
	case "DynamoDB_20120810.GetItem":
		if body["ConsistentRead"] != true {
			w.t.Fatal("authorization and receipt reads must be consistent")
		}
		key := body["Key"].(map[string]any)
		pk := key["PK"].(map[string]any)["S"].(string)
		sk := key["SK"].(map[string]any)["S"].(string)
		switch {
		case pk == "ACCOUNT#acct" && sk == "META" && w.account:
			result["Item"] = map[string]any{"PK": noteString(pk), "SK": noteString(sk)}
		case pk == "ACCOUNT#acct" && sk == "MEMBER#user" && w.member:
			result["Item"] = map[string]any{"PK": noteString(pk), "SK": noteString(sk)}
		case pk == "USER#user" && sk == "CONNECTOR_MUTATION#client#request" && w.contactReceipt != nil:
			result["Item"] = w.contactReceipt
		}
	case "DynamoDB_20120810.Query":
		if w.revokeOnQuery {
			w.member = false
		}
		result["Items"] = []any{}
		if w.contact != nil {
			result["Items"] = []any{w.contact}
		}
	case "DynamoDB_20120810.TransactWriteItems":
		items := body["TransactItems"].([]any)
		if len(items) == 5 {
			for i, sk := range []string{"POLICY", "CONNECTOR_APPROVAL#client", "META", "MEMBER#user", "CONNECTOR_MUTATION#client#request"} {
				check := items[i].(map[string]any)["ConditionCheck"].(map[string]any)
				if check["Key"].(map[string]any)["SK"].(map[string]any)["S"] != sk {
					w.t.Fatalf("missing probe check %s", sk)
				}
				if i == 4 && !strings.Contains(check["ConditionExpression"].(string), "attribute_not_exists") {
					w.t.Fatal("receipt absence not pinned")
				}
			}
			w.missingChecked = true
			if w.reject || !w.account || !w.member || w.contactReceipt != nil {
				status = 400
				result = map[string]any{"__type": "TransactionCanceledException", "CancellationReasons": []any{map[string]any{"Code": "ConditionalCheckFailed"}}}
			}
			break
		}
		if len(items) != 6 {
			w.t.Fatalf("expected 6 atomic operations, got %d", len(items))
		}
		for i, sk := range []string{"POLICY", "CONNECTOR_APPROVAL#client", "META", "MEMBER#user"} {
			check := items[i].(map[string]any)["ConditionCheck"].(map[string]any)
			if check["Key"].(map[string]any)["SK"].(map[string]any)["S"] != sk {
				w.t.Fatalf("missing atomic check %s", sk)
			}
		}
		for i, fields := range [][]string{{"clientId", "generation", "audience", "issuer", "enabled", "maximum"}, {"userId", "clientId", "generation", "revoked", "blocked", "expiresAt", "capabilities"}} {
			check := items[i].(map[string]any)["ConditionCheck"].(map[string]any)
			names := check["ExpressionAttributeNames"].(map[string]any)
			for _, field := range fields {
				found := false
				for token, name := range names {
					if name == field && strings.Contains(check["ConditionExpression"].(string), token) {
						found = true
					}
				}
				if !found {
					w.t.Fatalf("missing atomic authorization %s", field)
				}
			}
		}
		for _, i := range []int{2, 3} {
			if !strings.Contains(items[i].(map[string]any)["ConditionCheck"].(map[string]any)["ConditionExpression"].(string), "attribute_exists") {
				w.t.Fatal("missing canonical access check")
			}
		}
		put, create := items[4].(map[string]any)["Put"].(map[string]any)
		changed := !create && w.contact != nil && w.contactReceipt != nil && w.contact["revision"].(map[string]any)["S"] != w.contactReceipt["revision"].(map[string]any)["S"]
		if w.reject || !w.account || !w.member || (!create && (w.contact == nil || changed)) || (create && w.contactReceipt != nil) {
			status = 400
			result = map[string]any{"__type": "TransactionCanceledException", "CancellationReasons": []any{map[string]any{"Code": "ConditionalCheckFailed"}}}
		} else if create {
			if !strings.Contains(put["ConditionExpression"].(string), "attribute_not_exists") {
				w.t.Fatal("contact can overwrite")
			}
			w.writes++
			w.contact = put["Item"].(map[string]any)
			if _, plain := w.contact["name"]; plain || w.contact["sealed"] == nil {
				w.t.Fatal("connector contact written in plaintext")
			}
			w.contactReceipt = items[5].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
			if w.revokeAfterWrite {
				w.approval["revoked"] = noteBool(true)
			}
			if w.lose {
				w.lose = false
				status = 500
				result = map[string]any{"__type": "InternalServerError"}
			}
		} else {
			for _, i := range []int{4, 5} {
				if items[i].(map[string]any)["ConditionCheck"] == nil {
					w.t.Fatal("replay missing identity pin")
				}
			}
		}
	default:
		w.t.Fatalf("unexpected DynamoDB operation %s", target)
	}
	raw, _ := json.Marshal(result)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(string(raw))), Request: req}, nil
}
func contactBridge(t *testing.T, w *contactWire) (*connector.Bridge, func(string, string, string, string) []byte) {
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", HTTPClient: w, Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
	})})
	repo := repository.NewDynamoDBRepository(client, "table")
	if !w.noSealer {
		repo.SetRecordSealer(bridgeTestSealer{})
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
	router.Get("/api/accounts/{accountId}/contacts", handler.NewAccountContactsHandler(service.NewAccountContactsService(repo)).List)
	bridge := &connector.Bridge{Verify: verifier.Verify, Router: router, ContactMutate: handler.NewConnectorContactHandler(service.NewConnectorContactService(repo, noteAudience, noteIssuer)).Create}
	sign := func(method, path, body, operation string) []byte {
		args := []byte(fmt.Sprintf(`{"method":%q,"path":%q,"body":%s}`, method, path, body))
		hash := sha256.Sum256(args)
		now := time.Now().Unix()
		caps := "\"read\""
		generation := 0
		if method == "POST" {
			caps = "\"read\",\"write\""
			generation = 3
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
func TestConnectorContactBridgeCreateReplayAndReads(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint("lost=", lost), func(t *testing.T) {
			w := &contactWire{noteWire: newNoteWire(t), account: true, member: true, lose: lost}
			bridge, sign := contactBridge(t, w)
			call := func(body string) connector.InvokeResult {
				return bridge.Invoke(context.Background(), sign("POST", "/api/accounts/acct/contacts", body, "api.accountContacts.create"))
			}
			body := `{"contact":{"name":"홍길동","phone":"010-1234"},"idempotencyKey":"request"}`
			first := call(body)
			if first.StatusCode != 200 || w.writes != 1 {
				t.Fatalf("first=%+v writes=%d", first, w.writes)
			}
			id := w.contact["contactId"].(map[string]any)["S"].(string)
			revision := w.contact["revision"].(map[string]any)["S"].(string)
			if _, err := uuid.Parse(revision); err != nil {
				t.Fatal("missing revision")
			}
			replay := call(body)
			if replay.StatusCode != 200 || !strings.Contains(replay.Body, id) || !strings.Contains(replay.Body, `"replayed":true`) || w.writes != 1 {
				t.Fatalf("replay=%+v", replay)
			}
			changedBody := `{"idempotencyKey":"request","contact":{"name":"홍길동","phone":"010-1234"}}`
			if out := call(changedBody); out.StatusCode != 409 {
				t.Fatalf("different body bytes: %+v", out)
			}
			mismatch := call(strings.Replace(body, "홍길동", "다른 이름", 1))
			if mismatch.StatusCode != 409 {
				t.Fatalf("mismatch=%+v", mismatch)
			}
			probe := bridge.Invoke(context.Background(), sign("POST", "/api/accounts/acct/contacts?replay=1", body, "api.accountContacts.create"))
			if probe.StatusCode != 200 || !strings.Contains(probe.Body, id) {
				t.Fatalf("probe=%+v", probe)
			}
			list := bridge.Invoke(context.Background(), sign("GET", "/api/accounts/acct/contacts?limit=1", "null", "api.accountContacts.list"))
			if list.StatusCode != 200 || !strings.Contains(list.Body, id) {
				t.Fatalf("list=%+v", list)
			}
			w.contact["revision"] = noteString(uuid.NewString())
			modified := call(body)
			if modified.StatusCode != 409 || w.writes != 1 {
				t.Fatalf("modified replay=%+v", modified)
			}
			w.contact = nil
			deleted := call(body)
			if deleted.StatusCode != 409 || w.writes != 1 {
				t.Fatalf("deleted replay=%+v", deleted)
			}
			w.member = false
			denied := call(body)
			if denied.StatusCode != 403 {
				t.Fatalf("membership=%+v", denied)
			}
		})
	}
}
func TestConnectorContactBridgeRejectsRevocationAndOtherAccounts(t *testing.T) {
	w := &contactWire{noteWire: newNoteWire(t), account: true, member: true, revokeAfterWrite: true}
	bridge, sign := contactBridge(t, w)
	body := `{"contact":{"name":"A"},"idempotencyKey":"request"}`
	out := bridge.Invoke(context.Background(), sign("POST", "/api/accounts/acct/contacts", body, "api.accountContacts.create"))
	if out.StatusCode != 403 || w.writes != 1 || strings.Contains(out.Body, "contactId") {
		t.Fatalf("revocation leaked receipt %+v", out)
	}
	w.approval["revoked"] = noteBool(false)
	out = bridge.Invoke(context.Background(), sign("POST", "/api/accounts/other/contacts", body, "api.accountContacts.create"))
	if out.StatusCode != 403 || w.writes != 1 {
		t.Fatalf("cross account %+v", out)
	}
}

func TestConnectorContactRejectsInvalidBodiesAndBoundsRead(t *testing.T) {
	w := &contactWire{noteWire: newNoteWire(t), account: true, member: true}
	bridge, sign := contactBridge(t, w)
	invalid := []string{
		`{"contact":{"name":"A","name":"B"},"idempotencyKey":"request"}`,
		`{"contact":{"name":"A","revision":"client-owned"},"idempotencyKey":"request"}`,
		`{"contact":{"name":"A"},"idempotencyKey":"wrong"}`,
		`{"contact":{"name":null},"idempotencyKey":"request"}`,
	}
	oversize, _ := json.Marshal(map[string]any{"contact": map[string]string{"name": "A", "notes": strings.Repeat(`"`, 8192)}, "idempotencyKey": "request"})
	invalid = append(invalid, string(oversize))
	for _, body := range invalid {
		out := bridge.Invoke(context.Background(), sign("POST", "/api/accounts/acct/contacts", body, "api.accountContacts.create"))
		if out.StatusCode != 400 || w.writes != 0 {
			t.Fatalf("invalid input produced %d, writes %d", out.StatusCode, w.writes)
		}
	}
	const largeID = "01234567-89ab-4cde-8123-0123456789ab"
	sealed, _ := bridgeTestSealer{}.Seal(context.Background(), []byte(`{"contactId":"`+largeID+`","name":"`+strings.Repeat("x", 15000)+`"}`), map[string]string{"purpose": "ttobak-account-contact", "accountId": "acct", "contactId": largeID})
	w.contact = map[string]any{"PK": noteString("ACCOUNT#acct"), "SK": noteString("CONTACT#" + largeID), "contactId": noteString(largeID), "revision": noteString("r"), "sealed": noteString(sealed)}
	out := bridge.Invoke(context.Background(), sign("GET", "/api/accounts/acct/contacts?limit=1", "null", "api.accountContacts.list"))
	if out.StatusCode != 413 || strings.Contains(out.Body, strings.Repeat("x", 100)) {
		t.Fatalf("large read leaked bytes: %d", out.StatusCode)
	}
}

func TestConnectorContactAtomicAccessRaceFailsClosed(t *testing.T) {
	w := &contactWire{noteWire: newNoteWire(t), account: true, member: true, reject: true}
	bridge, sign := contactBridge(t, w)
	body := `{"contact":{"name":"A"},"idempotencyKey":"request"}`
	out := bridge.Invoke(context.Background(), sign("POST", "/api/accounts/acct/contacts", body, "api.accountContacts.create"))
	if out.StatusCode < 400 || w.writes != 0 {
		t.Fatalf("atomic rejection %+v", out)
	}
}

func TestConnectorContactReadRechecksMembership(t *testing.T) {
	w := &contactWire{noteWire: newNoteWire(t), account: true, member: true, revokeOnQuery: true}
	bridge, sign := contactBridge(t, w)
	out := bridge.Invoke(context.Background(), sign("GET", "/api/accounts/acct/contacts?limit=1", "null", "api.accountContacts.list"))
	if out.StatusCode != 403 {
		t.Fatalf("read after revocation: %+v", out)
	}
}

func TestConnectorContactMissingReplayChecksCurrentAccessAtomically(t *testing.T) {
	w := &contactWire{noteWire: newNoteWire(t), account: true, member: true}
	bridge, sign := contactBridge(t, w)
	body := `{"contact":{"name":"A"},"idempotencyKey":"request"}`
	out := bridge.Invoke(context.Background(), sign("POST", "/api/accounts/acct/contacts?replay=1", body, "api.accountContacts.create"))
	if out.StatusCode != 404 || w.writes != 0 {
		t.Fatalf("missing replay: %+v", out)
	}
	if !w.missingChecked {
		t.Fatal("missing receipt was not checked atomically with current access")
	}
	w.reject = true
	out = bridge.Invoke(context.Background(), sign("POST", "/api/accounts/acct/contacts?replay=1", body, "api.accountContacts.create"))
	if out.StatusCode != 409 || w.writes != 0 {
		t.Fatalf("rejected replay probe: %+v", out)
	}
}

func TestConnectorContactExpiredApprovalFailsClosed(t *testing.T) {
	w := &contactWire{noteWire: newNoteWire(t), account: true, member: true}
	w.approval["expiresAt"] = noteNumber(time.Now().Unix() - 1)
	bridge, sign := contactBridge(t, w)
	body := `{"contact":{"name":"A"},"idempotencyKey":"request"}`
	out := bridge.Invoke(context.Background(), sign("POST", "/api/accounts/acct/contacts", body, "api.accountContacts.create"))
	if out.StatusCode != 403 || w.writes != 0 {
		t.Fatalf("expired approval: %+v", out)
	}
}

// bridgeTestSealer is a reversible stand-in for KMS sealing that enforces the
// record binding like the real envelope.
type bridgeTestSealer struct{}

func (bridgeTestSealer) id(b map[string]string) string {
	return b["purpose"] + "/" + b["accountId"] + "/" + b["contactId"] + "|"
}
func (s bridgeTestSealer) Seal(_ context.Context, p []byte, b map[string]string) (string, error) {
	return base64.StdEncoding.EncodeToString(append([]byte(s.id(b)), p...)), nil
}
func (s bridgeTestSealer) Open(_ context.Context, v string, b map[string]string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil || !strings.HasPrefix(string(raw), s.id(b)) {
		return nil, sealing.ErrInvalid
	}
	return raw[len(s.id(b)):], nil
}

func TestConnectorContactCreateFailsClosedWithoutEncryptionKey(t *testing.T) {
	w := &contactWire{noteWire: newNoteWire(t), account: true, member: true, noSealer: true}
	bridge, sign := contactBridge(t, w)
	out := bridge.Invoke(context.Background(), sign("POST", "/api/accounts/acct/contacts", `{"contact":{"name":"A"},"idempotencyKey":"request"}`, "api.accountContacts.create"))
	if out.StatusCode != 503 || !strings.Contains(out.Body, "CONTACTS_UNAVAILABLE") || w.writes != 0 {
		t.Fatalf("unconfigured key: %d %s writes=%d", out.StatusCode, out.Body, w.writes)
	}
}
