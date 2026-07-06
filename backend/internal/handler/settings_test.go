package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/ttobak/backend/internal/service"
)

// decryptStoredAPIKey must pass the stored value through unchanged when no crypto
// service is configured, and must not silently swallow a real decrypt failure —
// that was the bug: export/research handlers sent the KMS-encrypted ciphertext
// straight to Notion as the bearer token because every decrypt error, real or
// not, fell back to the raw (encrypted) stored value.
func TestDecryptStoredAPIKey(t *testing.T) {
	ctx := context.Background()

	t.Run("no crypto service passes through unchanged", func(t *testing.T) {
		got, err := decryptStoredAPIKey(ctx, nil, "secret_plaintext_key_1234567890")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "secret_plaintext_key_1234567890" {
			t.Fatalf("expected pass-through of stored key, got %q", got)
		}
	})

	// Missing region makes every Decrypt call fail immediately (no network call),
	// which deterministically exercises the decrypt-failure branch.
	brokenCrypto := service.NewCryptoService(kms.NewFromConfig(aws.Config{}), "test-key")

	t.Run("decrypt failure falls back for legacy plaintext keys", func(t *testing.T) {
		got, err := decryptStoredAPIKey(ctx, brokenCrypto, "secret_legacy_plaintext_key12")
		if err != nil {
			t.Fatalf("expected legacy pass-through, got error: %v", err)
		}
		if got != "secret_legacy_plaintext_key12" {
			t.Fatalf("expected pass-through of legacy key, got %q", got)
		}
	})

	t.Run("decrypt failure surfaces error for real ciphertext", func(t *testing.T) {
		_, err := decryptStoredAPIKey(ctx, brokenCrypto, "not-a-valid-notion-key-format")
		if err == nil {
			t.Fatal("expected error for undecryptable ciphertext, got nil")
		}
	})
}

// SaveNotionKey must reject a missing or unparseable parentPage before ever
// touching crypto/repo/Notion — this is the fix for internal integrations
// being unable to create pages at the workspace root: every export now
// requires a verified parent page, so save-time is where that's enforced.
func TestSaveNotionKeyRequiresParentPage(t *testing.T) {
	h := NewSettingsHandler(nil, nil, nil)

	cases := []struct {
		name string
		body string
	}{
		{"missing parentPage", `{"apiKey":"secret_1234567890"}`},
		{"unparseable parentPage", `{"apiKey":"secret_1234567890","parentPage":"not-a-notion-url"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/settings/integrations/notion", strings.NewReader(tc.body))
			w := httptest.NewRecorder()

			h.SaveNotionKey(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
