package handler

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type persistedCalendarKMS struct{ cipher cipher.AEAD }

func (fake persistedCalendarKMS) context(key *string, binding map[string]string) ([]byte, error) {
	if aws.ToString(key) != "synthetic-calendar-key" || len(binding) != 4 ||
		binding["purpose"] != "ttobak-calendar-event-v1" ||
		(binding["userId"] != "owner" && binding["userId"] != "other") ||
		binding["sourceId"] == "" || len(binding["eventId"]) != 64 {
		return nil, errors.New("invalid synthetic KMS context")
	}
	return json.Marshal(binding)
}

func (fake persistedCalendarKMS) Encrypt(_ context.Context, input *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	binding, err := fake.context(input.KeyId, input.EncryptionContext)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, fake.cipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return &kms.EncryptOutput{CiphertextBlob: fake.cipher.Seal(nonce, nonce, input.Plaintext, binding)}, nil
}

func (fake persistedCalendarKMS) Decrypt(_ context.Context, input *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	binding, err := fake.context(input.KeyId, input.EncryptionContext)
	if err != nil || len(input.CiphertextBlob) < fake.cipher.NonceSize() {
		return nil, errors.New("invalid synthetic KMS envelope")
	}
	plaintext, err := fake.cipher.Open(nil, input.CiphertextBlob[:fake.cipher.NonceSize()], input.CiphertextBlob[fake.cipher.NonceSize():], binding)
	if err != nil {
		return nil, err
	}
	return &kms.DecryptOutput{Plaintext: plaintext}, nil
}

type persistedCalendarAccounts struct{}

func (persistedCalendarAccounts) GetAccount(_ context.Context, userID, accountID string) (*model.AccountResponse, error) {
	if (userID != "owner" && userID != "other") || accountID != "11111111-1111-4111-8111-111111111111" {
		return nil, service.ErrForbidden
	}
	return &model.AccountResponse{AccountID: accountID}, nil
}

func TestCalendarPersistedStdioAcceptance(test *testing.T) {
	endpoint := os.Getenv("TTOBAK_CALENDAR_DYNAMODB_ENDPOINT")
	if endpoint == "" {
		test.Skip("set TTOBAK_CALENDAR_DYNAMODB_ENDPOINT=http://127.0.0.1:32887 for local persisted stdio acceptance")
	}
	if endpoint != "http://127.0.0.1:32887" {
		test.Fatal("calendar acceptance only permits the controller's explicit loopback DynamoDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := dynamodb.New(dynamodb.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(endpoint),
		Credentials: credentials.NewStaticCredentialsProvider("synthetic", "synthetic", ""),
		Retryer:     aws.NopRetryer{},
	})
	table := "calendar-acceptance-" + uuid.NewString()
	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(table), BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("SK"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("SK"), KeyType: types.KeyTypeRange},
		},
	})
	if err != nil {
		test.Fatal(err)
	}
	defer func() {
		cleanup, finish := context.WithTimeout(context.Background(), 10*time.Second)
		defer finish()
		if _, err := client.DeleteTable(cleanup, &dynamodb.DeleteTableInput{TableName: aws.String(table)}); err != nil {
			test.Errorf("delete synthetic table: %v", err)
		}
	}()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		test.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		test.Fatal(err)
	}
	authenticatedCipher, err := cipher.NewGCM(block)
	if err != nil {
		test.Fatal(err)
	}
	crypto := service.NewCalendarCipher(persistedCalendarKMS{cipher: authenticatedCipher}, "synthetic-calendar-key")
	calendar := NewCalendarHandler(service.NewCalendarService(repository.NewCalendarRepository(client, table), crypto, persistedCalendarAccounts{}))
	ownerToken, otherToken := uuid.NewString(), uuid.NewString()
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			userID := ""
			switch request.Header.Get("Authorization") {
			case "Bearer " + ownerToken:
				userID = "owner"
			case "Bearer " + otherToken:
				userID = "other"
			default:
				http.Error(writer, "synthetic authentication required", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), middleware.UserIDKey, userID)))
		})
	})
	router.Post("/api/calendar/sources/{sourceId}/sync", calendar.Sync)
	router.Get("/api/calendar/events", calendar.ListEvents)
	router.Get("/api/calendar/sources", calendar.ListSources)
	router.Get("/fixture/persisted", func(writer http.ResponseWriter, request *http.Request) {
		rows, err := inspectPersistedCalendar(request.Context(), client, table, crypto)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(rows)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	root, err := filepath.Abs("../../..")
	if err != nil {
		test.Fatal(err)
	}
	command := exec.CommandContext(ctx, "node", "test/fixtures/calendar-persisted-driver.mjs")
	command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
	command.WaitDelay = 3 * time.Second
	command.Dir = filepath.Join(root, "mcp-server")
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"), "TMPDIR=/tmp",
		"CALENDAR_PERSISTED_URL=" + server.URL,
		"CALENDAR_OWNER_TOKEN=" + ownerToken, "CALENDAR_OTHER_TOKEN=" + otherToken,
	}
	output, err := command.CombinedOutput()
	test.Log(string(output))
	if err != nil {
		test.Fatalf("stdio-to-handler-to-repository acceptance: %v", err)
	}
}

func inspectPersistedCalendar(ctx context.Context, client *dynamodb.Client, table string, crypto *service.CalendarCipher) ([]map[string]any, error) {
	var rows []map[string]any
	canonical := make(map[string]repository.CalendarEventRow)
	references := make(map[string]map[string]any)
	paginator := dynamodb.NewScanPaginator(client, &dynamodb.ScanInput{TableName: aws.String(table), ConsistentRead: aws.Bool(true)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			var row map[string]any
			if err := attributevalue.UnmarshalMap(item, &row); err != nil {
				return nil, err
			}
			sortKey := row["SK"].(string)
			encoded, _ := json.Marshal(row)
			if strings.Contains(string(encoded), "Synthetic private") || row["title"] != nil || row["location"] != nil || row["externalId"] != nil {
				return nil, errors.New("plaintext persisted outside authenticated ciphertext")
			}
			if strings.HasPrefix(sortKey, "CALEVENT#") {
				var event repository.CalendarEventRow
				if err := attributevalue.UnmarshalMap(item, &event); err != nil {
					return nil, err
				}
				plaintext, err := crypto.Decrypt(ctx, event.UserID, event.SourceID, event.EventID, event.Ciphertext)
				if err != nil || !json.Valid(plaintext) {
					return nil, errors.New("persisted ciphertext failed authenticated context read")
				}
				other := "other"
				if event.UserID == other {
					other = "owner"
				}
				if _, err := crypto.Decrypt(ctx, other, event.SourceID, event.EventID, event.Ciphertext); err == nil {
					return nil, errors.New("ciphertext accepted a different owner context")
				}
				canonical[event.PK+"/"+model.CalendarTimeSortKey(event.StartsAt, event.SourceID, event.EventID)] = event
			}
			if strings.HasPrefix(sortKey, "CALTIME#") {
				references[row["PK"].(string)+"/"+sortKey] = row
			}
			rows = append(rows, row)
		}
	}
	if len(canonical) != len(references) {
		return nil, fmt.Errorf("canonical/reference mismatch: %d/%d", len(canonical), len(references))
	}
	for key, event := range canonical {
		ref := references[key]
		if ref == nil || ref["eventId"] != event.EventID || ref["sourceId"] != event.SourceID ||
			ref["revision"] != float64(event.Revision) || ref["pendingShareExpiresAt"] != float64(event.ExpiresAt) {
			return nil, errors.New("canonical chronological reference identity mismatch")
		}
	}
	return rows, nil
}
