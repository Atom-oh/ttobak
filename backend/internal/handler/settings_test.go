package handler

import "testing"

// decryptStoredAPIKey must pass the stored value through unchanged when no
// crypto service is configured — this was the bug: export/research handlers
// sent the KMS-encrypted ciphertext straight to Notion as the bearer token.
func TestDecryptStoredAPIKey_NoCryptoPassesThrough(t *testing.T) {
	got := decryptStoredAPIKey(nil, nil, "secret_plaintext_key")
	if got != "secret_plaintext_key" {
		t.Fatalf("expected pass-through of stored key, got %q", got)
	}
}
