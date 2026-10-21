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
	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/handler"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

const noteAudience = "https://example.invalid/api/mcp"
const noteIssuer = "https://cognito-idp.ap-northeast-2.amazonaws.com/ap-northeast-2_test"
const noteKeyARN = "arn:aws:kms:ap-northeast-2:111111111111:key/00000000-0000-0000-0000-000000000000"
const noteBody = `{"title":"노트","markdown":"본문 [[다음 문서]]","docType":"note","idempotencyKey":"request"}`

type noteWire struct {
	t                              *testing.T
	policy, approval, doc, receipt map[string]any
	creates, confirms              int
	loseCreate, rejectTransaction  bool
	afterCreate                    func()
}

func noteString(value string) map[string]any { return map[string]any{"S": value} }
func noteBool(value bool) map[string]any     { return map[string]any{"BOOL": value} }
func noteNumber(value int64) map[string]any  { return map[string]any{"N": fmt.Sprint(value)} }
func noteSS() map[string]any                 { return map[string]any{"SS": []string{"read", "write"}} }

func newNoteWire(t *testing.T) *noteWire {
	return &noteWire{t: t, policy: map[string]any{
		"PK": noteString("CONNECTOR_CLIENT#client"), "SK": noteString("POLICY"),
		"clientId": noteString("client"), "enabled": noteBool(true), "maximum": noteSS(),
		"audience": noteString(noteAudience), "issuer": noteString(noteIssuer), "generation": noteNumber(2),
	}, approval: map[string]any{
		"PK": noteString("USER#user"), "SK": noteString("CONNECTOR_APPROVAL#client"),
		"userId": noteString("user"), "clientId": noteString("client"),
		"generation": noteNumber(3), "expiresAt": noteNumber(time.Now().Unix() + 3600),
		"capabilities": noteSS(), "revoked": noteBool(false), "blocked": noteBool(false),
	}}
}

func (w *noteWire) Do(request *http.Request) (*http.Response, error) {
	w.t.Helper()
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		w.t.Fatal(err)
	}
	status, result := 200, map[string]any{}
	switch request.Header.Get("X-Amz-Target") {
	case "DynamoDB_20120810.TransactGetItems":
		result["Responses"] = []any{map[string]any{"Item": w.policy}, map[string]any{"Item": w.approval}}
	case "DynamoDB_20120810.GetItem":
		if body["ConsistentRead"] != true {
			w.t.Fatal("receipt reconciliation must be strongly consistent")
		}
		key := body["Key"].(map[string]any)
		if key["PK"].(map[string]any)["S"] != "USER#user" ||
			key["SK"].(map[string]any)["S"] != "CONNECTOR_MUTATION#client#request" {
			w.t.Fatal("receipt escaped the verified user/client/request")
		}
		if w.receipt != nil {
			result["Item"] = w.receipt
		}
	case "DynamoDB_20120810.TransactWriteItems":
		items := body["TransactItems"].([]any)
		if len(items) != 4 {
			w.t.Fatal("authorization and note/receipt must form one transaction")
		}
		for index, expected := range []map[string]any{
			{"generation": noteNumber(2), "audience": noteString(noteAudience), "issuer": noteString(noteIssuer), "enabled": noteBool(true)},
			{"generation": noteNumber(3), "expiresAt": nil, "blocked": noteBool(false), "revoked": noteBool(false)},
		} {
			check := items[index].(map[string]any)["ConditionCheck"].(map[string]any)
			names := check["ExpressionAttributeNames"].(map[string]any)
			for field := range expected {
				found := false
				for token, name := range names {
					found = found || name == field && strings.Contains(check["ConditionExpression"].(string), token)
				}
				if !found {
					w.t.Fatalf("missing atomic authorization pin: %s", field)
				}
			}
		}
		put, create := items[2].(map[string]any)["Put"].(map[string]any)
		if w.rejectTransaction || (!create && w.doc == nil) {
			status = 400
			result = map[string]any{"__type": "TransactionCanceledException",
				"CancellationReasons": []any{map[string]any{"Code": "ConditionalCheckFailed"}}}
		} else if create {
			w.creates++
			if w.doc != nil || w.receipt != nil || !strings.Contains(put["ConditionExpression"].(string), "attribute_not_exists") {
				w.t.Fatal("creation could overwrite existing content")
			}
			w.doc = put["Item"].(map[string]any)
			receiptPut := items[3].(map[string]any)["Put"].(map[string]any)
			w.receipt = receiptPut["Item"].(map[string]any)
			if w.afterCreate != nil {
				w.afterCreate()
			}
			if w.loseCreate {
				status, result = 500, map[string]any{"__type": "InternalServerError", "message": "private detail"}
			}
		} else {
			w.confirms++
			if items[3].(map[string]any)["ConditionCheck"] == nil {
				w.t.Fatal("replay omitted the exact receipt condition")
			}
		}
	default:
		w.t.Fatalf("unexpected DynamoDB operation: %s", request.Header.Get("X-Amz-Target"))
	}
	raw, _ := json.Marshal(result)
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/x-amz-json-1.0"}},
		Body: io.NopCloser(strings.NewReader(string(raw))), Request: request}, nil
}

func noteBridge(t *testing.T, wire *noteWire) (*connector.Bridge, func(string) []byte) {
	t.Helper()
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", HTTPClient: wire,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
		})})
	repo := repository.NewDynamoDBRepository(client, "table")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	spki, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	verifier, err := connector.NewVerifier(noteKeyARN, spki, "ttobak-api-bridge",
		repo.ConnectorGrantResolver(noteAudience, noteIssuer), nil)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &connector.Bridge{Verify: verifier.Verify,
		Router: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("mutation reached legacy router") }),
		Mutate: handler.NewConnectorNoteHandler(service.NewConnectorNoteService(repo, noteAudience, noteIssuer)).Create}
	sign := func(body string) []byte {
		args := []byte(`{"method":"POST","path":"/api/documents","body":` + body + `}`)
		argHash := sha256.Sum256(args)
		now := time.Now().Unix()
		// Canonical field order is the public signed wire contract, not a forged Principal.
		raw := []byte(fmt.Sprintf(`{"version":1,"protocol":"ttobak.connector","destination":"ttobak-api-bridge","sub":"user","clientId":"client","capabilities":["read","write"],"approvalGeneration":3,"operation":"api.document.putDocument","argumentsSHA256":"%s","requestId":"request","issuedAt":%d,"expiresAt":%d,"upstreamExpiresAt":%d}`,
			hex.EncodeToString(argHash[:]), now, now+55, now+3600))
		digest, err := connector.SigningDigest(raw)
		if err != nil {
			t.Fatal(err)
		}
		signature, err := rsa.SignPSS(rand.Reader, key, crypto.SHA256, digest, &rsa.PSSOptions{SaltLength: 32})
		if err != nil {
			t.Fatal(err)
		}
		envelope := []byte(fmt.Sprintf(`{"keyId":"%s","algorithm":"RSASSA_PSS_SHA_256","payload":"%s","signature":"%s"}`,
			noteKeyARN, connector.EncodeBridgeBytes(raw), connector.EncodeBridgeBytes(signature)))
		return connector.EncodeInvokeFrame(connector.InvokeFrame{Protocol: connector.InvokeProtocol, Operation: "api.document.putDocument",
			RequestID: "request", Delegation: connector.EncodeBridgeBytes(envelope), Arguments: connector.EncodeBridgeBytes(args)})
	}
	return bridge, sign
}

func TestConnectorNoteCreatesOnceAndReconcilesAmbiguousResponse(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint("lost-response=", lost), func(t *testing.T) {
			wire := newNoteWire(t)
			wire.loseCreate = lost
			bridge, sign := noteBridge(t, wire)
			frame := sign(noteBody)
			first := bridge.Invoke(context.Background(), frame)
			if first.StatusCode != 200 || wire.creates != 1 {
				t.Fatalf("create: %+v calls=%d", first, wire.creates)
			}
			var receipt model.ConnectorNoteReceipt
			if json.Unmarshal([]byte(first.Body), &receipt) != nil ||
				receipt.DocID != model.ConnectorNoteID("user", "client", "request") || receipt.Replayed != lost {
				t.Fatalf("wrong receipt: %+v", receipt)
			}
			for field, want := range map[string]string{"PK": "USER#user", "sourceUserId": "user", "entityType": model.EntityTypeUserDoc, "docType": "note", "title": "노트"} {
				if wire.doc[field].(map[string]any)["S"] != want {
					t.Fatalf("wrong stored %s", field)
				}
			}
			wire.doc["content"] = noteString("later human edit")
			second := bridge.Invoke(context.Background(), sign(noteBody))
			if second.StatusCode != 200 || !strings.Contains(second.Body, `"replayed":true`) || wire.creates != 1 ||
				wire.doc["content"].(map[string]any)["S"] != "later human edit" {
				t.Fatalf("retry overwrote note: %+v", second)
			}
			conflict := bridge.Invoke(context.Background(), sign(strings.Replace(noteBody, "노트", "changed", 1)))
			if conflict.StatusCode != 409 || wire.creates != 1 {
				t.Fatalf("same key accepted different bytes: %+v", conflict)
			}
			wire.doc = nil
			deleted := bridge.Invoke(context.Background(), sign(noteBody))
			if deleted.StatusCode != 409 || wire.creates != 1 {
				t.Fatalf("replay recreated a deleted note: %+v", deleted)
			}
		})
	}
}

func TestConnectorNoteInvalidBodiesNeverWrite(t *testing.T) {
	wire := newNoteWire(t)
	bridge, sign := noteBridge(t, wire)
	for _, body := range []string{
		`null`, `[]`, `{}`, noteBody + ` {}`,
		strings.Replace(noteBody, `"title":`, `"title":"duplicate","title":`, 1),
		strings.Replace(noteBody, `"노트"`, `null`, 1),
		strings.Replace(noteBody, `"note"`, `"slide"`, 1),
		strings.Replace(noteBody, `"request"`, `"different"`, 1),
		strings.Replace(noteBody, `"title":`, `"accountId":"foreign","title":`, 1),
		strings.Replace(noteBody, `"노트"`, `"`+strings.Repeat("한", 171)+`"`, 1),
		strings.Replace(noteBody, `"본문 [[다음 문서]]"`, `"`+strings.Repeat("한", 20001)+`"`, 1),
	} {
		if got := bridge.Invoke(context.Background(), sign(body)); got.StatusCode < 400 || wire.creates != 0 {
			t.Fatalf("invalid body wrote: status=%d", got.StatusCode)
		}
	}
}

func TestConnectorNoteCurrentGrantAndAtomicConflictNeverPublish(t *testing.T) {
	for _, scenario := range []string{"disabled", "blocked", "revoked", "stale-generation", "expired", "commit-conflict", "revoke-after-commit"} {
		t.Run(scenario, func(t *testing.T) {
			wire := newNoteWire(t)
			bridge, sign := noteBridge(t, wire)
			frame := sign(noteBody)
			switch scenario {
			case "disabled":
				wire.policy["enabled"] = noteBool(false)
			case "blocked":
				wire.approval["blocked"] = noteBool(true)
			case "revoked":
				wire.approval["revoked"] = noteBool(true)
			case "stale-generation":
				wire.approval["generation"] = noteNumber(4)
			case "expired":
				wire.approval["expiresAt"] = noteNumber(time.Now().Unix() - 1)
			case "commit-conflict":
				wire.rejectTransaction = true
			case "revoke-after-commit":
				wire.afterCreate = func() { wire.approval["revoked"] = noteBool(true) }
			}
			got := bridge.Invoke(context.Background(), frame)
			want, creates := 403, 0
			if scenario == "commit-conflict" {
				want = 409
			} else if scenario == "revoke-after-commit" {
				creates = 1
			}
			if got.StatusCode != want || wire.creates != creates || strings.Contains(got.Body, `"docId"`) {
				t.Fatalf("grant/receipt boundary: %+v creates=%d", got, wire.creates)
			}
		})
	}
}
