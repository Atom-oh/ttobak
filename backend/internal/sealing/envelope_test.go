package sealing

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// fakeKMS wraps data keys by XOR with a context-derived pad so a wrong
// encryption context fails like real KMS does.
type fakeKMS struct {
	keys      map[string][]byte
	generated int
	decrypts  int
}

func contextID(c map[string]string) string {
	b, _ := aad(c)
	return string(b)
}

func (f *fakeKMS) GenerateDataKey(_ context.Context, in *kms.GenerateDataKeyInput, _ ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error) {
	f.generated++
	if in.KeySpec != kmstypes.DataKeySpecAes256 || len(in.EncryptionContext) == 0 {
		return nil, errors.New("bad request")
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	handle := make([]byte, 16)
	_, _ = rand.Read(handle)
	if f.keys == nil {
		f.keys = map[string][]byte{}
	}
	f.keys[string(handle)+contextID(in.EncryptionContext)] = append([]byte(nil), key...)
	return &kms.GenerateDataKeyOutput{Plaintext: key, CiphertextBlob: handle}, nil
}

func (f *fakeKMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	f.decrypts++
	key, ok := f.keys[string(in.CiphertextBlob)+contextID(in.EncryptionContext)]
	if !ok {
		return nil, &kmstypes.InvalidCiphertextException{}
	}
	return &kms.DecryptOutput{Plaintext: append([]byte(nil), key...)}, nil
}

func binding(id string) map[string]string {
	return map[string]string{"purpose": "account-contact", "accountId": "acct", "recordId": id}
}

func TestEnvelopeRoundTripAndRecordBinding(t *testing.T) {
	f := &fakeKMS{}
	e := NewEnvelope(f, "key")
	secret := []byte(`{"name":"홍길동","phone":"010-0000-0000"}`)
	sealed, err := e.Seal(context.Background(), secret, binding("c1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "홍길동") || bytes.Contains([]byte(sealed), []byte("010")) {
		t.Fatal("plaintext leaked into the sealed value")
	}
	got, err := e.Open(context.Background(), sealed, binding("c1"))
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("round trip: %q %v", got, err)
	}
	if _, err := e.Open(context.Background(), sealed, binding("c2")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a value moved to another record must not open: %v", err)
	}
	other := maps.Clone(binding("c1"))
	other["accountId"] = "acct-2"
	if _, err := e.Open(context.Background(), sealed, other); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a value moved to another account must not open: %v", err)
	}
	again, _ := e.Seal(context.Background(), secret, binding("c1"))
	if again == sealed || f.generated != 2 {
		t.Fatal("each seal must use a fresh data key and nonce")
	}
}

func TestEnvelopeRejectsTamperingAndMissingKey(t *testing.T) {
	e := NewEnvelope(&fakeKMS{}, "key")
	sealed, _ := e.Seal(context.Background(), []byte("x"), binding("c1"))
	raw, _ := base64.StdEncoding.DecodeString(sealed)
	raw[len(raw)-1] ^= 1
	for _, bad := range []string{base64.StdEncoding.EncodeToString(raw), "", "!!", base64.StdEncoding.EncodeToString([]byte{2, 0, 1})} {
		if _, err := e.Open(context.Background(), bad, binding("c1")); !errors.Is(err, ErrInvalid) {
			t.Fatalf("tampered value opened: %v", err)
		}
	}
	var missing *Envelope = NewEnvelope(&fakeKMS{}, "")
	if _, err := missing.Seal(context.Background(), []byte("x"), binding("c1")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unconfigured key must fail closed: %v", err)
	}
	if _, err := e.Seal(context.Background(), []byte("x"), nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("an unbound seal must be refused")
	}
}
