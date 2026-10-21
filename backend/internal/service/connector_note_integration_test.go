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
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/google/uuid"
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
	afterMissingReceipt            func()
	share                          map[string]any
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
		pk, sk := key["PK"].(map[string]any)["S"].(string), key["SK"].(map[string]any)["S"].(string)
		switch {
		case pk == "USER#user" && sk == "CONNECTOR_MUTATION#client#request":
			if w.receipt != nil {
				result["Item"] = w.receipt
			} else if w.afterMissingReceipt != nil {
				after := w.afterMissingReceipt
				w.afterMissingReceipt = nil
				after()
			}
		case strings.HasPrefix(sk, "DOC#") && (pk == "USER#user" || pk == "USER#recipient"):
			if w.doc != nil && w.doc["PK"].(map[string]any)["S"] == pk && w.doc["SK"].(map[string]any)["S"] == sk {
				result["Item"] = w.doc
			}
		case pk == "USER#recipient" && strings.HasPrefix(sk, model.PrefixDocShare):
			if w.share != nil && w.share["SK"].(map[string]any)["S"] == sk {
				result["Item"] = w.share
			}
		default:
			w.t.Fatalf("unexpected read scope: %s %s", pk, sk)
		}
	case "DynamoDB_20120810.Query":
		values := body["ExpressionAttributeValues"].(map[string]any)
		recipient, prefix := false, ""
		for _, value := range values {
			text, _ := value.(map[string]any)["S"].(string)
			recipient = recipient || text == "USER#recipient"
			if text == "DOC#" || text == model.PrefixDocShare {
				prefix = text
			}
		}
		if !recipient || prefix == "" {
			w.t.Fatal("unexpected document query scope")
		}
		result["Items"] = []any{}
		if prefix == model.PrefixDocShare && w.share != nil {
			result["Items"] = []any{w.share}
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
		if w.rejectTransaction || (!create && w.doc == nil) || (create && (w.doc != nil || w.receipt != nil)) {
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
			check := items[2].(map[string]any)["ConditionCheck"].(map[string]any)
			if check["Key"].(map[string]any)["SK"].(map[string]any)["S"] != w.doc["SK"].(map[string]any)["S"] {
				w.t.Fatal("replay did not target the receipt document")
			}
			for _, index := range []int{2, 3} {
				check := items[index].(map[string]any)["ConditionCheck"].(map[string]any)
				pinned := false
				for _, value := range check["ExpressionAttributeValues"].(map[string]any) {
					pinned = pinned || value.(map[string]any)["S"] == w.receipt["docId"].(map[string]any)["S"]
				}
				if !pinned {
					w.t.Fatal("replay condition omitted stored receipt identity")
				}
			}
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
				!model.ValidConnectorNoteReceiptID(receipt.DocID) || receipt.DocID != wire.doc["docId"].(map[string]any)["S"] || receipt.Replayed != lost {
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

func noteRepository(wire *noteWire) *repository.DynamoDBRepository {
	return repository.NewDynamoDBRepository(dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", HTTPClient: wire,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, nil
		})}), "table")
}

func TestConnectorNoteExpiredReceiptRemovalCannotResurrectOrphanShare(t *testing.T) {
	wire := newNoteWire(t)
	bridge, sign := noteBridge(t, wire)
	first := bridge.Invoke(context.Background(), sign(noteBody))
	if first.StatusCode != 200 {
		t.Fatalf("first create: %+v", first)
	}
	oldID := wire.doc["docId"].(map[string]any)["S"].(string)
	wire.share = map[string]any{"PK": noteString("USER#recipient"), "SK": noteString(model.PrefixDocShare + oldID),
		"meetingId": noteString(oldID), "ownerId": noteString("user"), "ownerEmail": noteString("owner@example.invalid"), "sharedToId": noteString("recipient"), "permission": noteString("read")}
	repo := noteRepository(wire)
	accounts := service.NewAccountService(repo, nil, "")
	reading := service.NewDocumentReadingService(repo)
	if doc, err := accounts.GetUserDocument(context.Background(), "recipient", oldID); err != nil || doc.Content == "" {
		t.Fatalf("initial shared document missing: %v", err)
	}
	if _, err := reading.Read(context.Background(), "recipient", "", oldID, nil); err != nil {
		t.Fatalf("initial bounded read failed: %v", err)
	}
	// The current deletion path intentionally leaves email-share rows behind.
	// Simulate deletion and later DynamoDB TTL removal of the retry receipt.
	wire.doc = nil
	wire.receipt = nil
	next := bridge.Invoke(context.Background(), sign(strings.Replace(noteBody, "본문", "new private body", 1)))
	if next.StatusCode != 200 {
		t.Fatalf("fresh post-TTL creation: %+v", next)
	}
	newID := wire.doc["docId"].(map[string]any)["S"].(string)
	if newID == oldID {
		t.Fatal("post-TTL creation reused the document ID protected by an orphan share")
	}
	for _, id := range []string{oldID, newID} {
		if _, err := accounts.GetUserDocument(context.Background(), "recipient", id); !errors.Is(err, service.ErrNotFound) {
			t.Fatalf("orphan detail read %s: %v", id, err)
		}
		if _, err := reading.Read(context.Background(), "recipient", "", id, nil); !errors.Is(err, service.ErrNotFound) {
			t.Fatalf("orphan bounded read %s: %v", id, err)
		}
	}
	if docs, err := accounts.ListUserDocuments(context.Background(), "recipient", ""); err != nil || len(docs) != 0 {
		t.Fatalf("orphan appeared in list: %v %v", docs, err)
	}
}

func TestConnectorNoteReplayUsesStoredIdentityAndRejectsExpiredReceipt(t *testing.T) {
	for _, id := range []string{"00112233445566778899aabbccddeeff", uuid.NewString()} {
		t.Run(id, func(t *testing.T) {
			wire := newNoteWire(t)
			bridge, sign := noteBridge(t, wire)
			if got := bridge.Invoke(context.Background(), sign(noteBody)); got.StatusCode != 200 {
				t.Fatalf("seed: %+v", got)
			}
			wire.doc["docId"] = noteString(id)
			wire.doc["SK"] = noteString("DOC#" + id)
			wire.receipt["docId"] = noteString(id)
			wire.doc["content"] = noteString("human content retained")
			got := bridge.Invoke(context.Background(), sign(noteBody))
			var receipt model.ConnectorNoteReceipt
			if got.StatusCode != 200 || json.Unmarshal([]byte(got.Body), &receipt) != nil || receipt.DocID != id || !receipt.Replayed {
				t.Fatalf("stored identity replay: %+v", got)
			}
			wire.receipt["pendingShareExpiresAt"] = noteNumber(time.Now().Unix() - 1)
			got = bridge.Invoke(context.Background(), sign(noteBody))
			if got.StatusCode != 409 || wire.creates != 1 || wire.doc["content"].(map[string]any)["S"] != "human content retained" {
				t.Fatalf("expired receipt accepted or edited content: %+v", got)
			}
		})
	}
}

func TestConnectorNoteConcurrentWinnerReceiptOverridesLosingCandidate(t *testing.T) {
	wire := newNoteWire(t)
	bridge, sign := noteBridge(t, wire)
	if got := bridge.Invoke(context.Background(), sign(noteBody)); got.StatusCode != 200 {
		t.Fatalf("seed: %+v", got)
	}
	winnerDoc, winnerReceipt := wire.doc, wire.receipt
	winnerID := uuid.NewString()
	winnerDoc["docId"] = noteString(winnerID)
	winnerDoc["SK"] = noteString("DOC#" + winnerID)
	winnerReceipt["docId"] = noteString(winnerID)
	wire.doc = nil
	wire.receipt = nil
	wire.afterMissingReceipt = func() { wire.doc = winnerDoc; wire.receipt = winnerReceipt }
	got := bridge.Invoke(context.Background(), sign(noteBody))
	var receipt model.ConnectorNoteReceipt
	if got.StatusCode != 200 || json.Unmarshal([]byte(got.Body), &receipt) != nil || receipt.DocID != winnerID || !receipt.Replayed || wire.creates != 1 {
		t.Fatalf("concurrent winner not reconciled: %+v", got)
	}
}
