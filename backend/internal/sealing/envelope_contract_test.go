package sealing

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
)

type sealingVector struct {
	Name          string            `json:"name"`
	DataKeyHex    string            `json:"dataKeyHex"`
	WrappedKeyHex string            `json:"wrappedKeyHex"`
	NonceHex      string            `json:"nonceHex"`
	Binding       map[string]string `json:"binding"`
	PlaintextUTF8 string            `json:"plaintextUtf8"`
	SealedBase64  string            `json:"sealedBase64"`
}

// vectorKMS decrypts exactly one wrapped key under exactly one context.
type vectorKMS struct {
	dataKey, wrapped []byte
	binding          map[string]string
}

func (v *vectorKMS) GenerateDataKey(context.Context, *kms.GenerateDataKeyInput, ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error) {
	return nil, errors.New("not used")
}

func (v *vectorKMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	if !bytes.Equal(in.CiphertextBlob, v.wrapped) {
		return nil, errors.New("unexpected wrapped key")
	}
	if !maps.Equal(in.EncryptionContext, v.binding) {
		return nil, errors.New("encryption context differs from binding")
	}
	return &kms.DecryptOutput{Plaintext: append([]byte(nil), v.dataKey...)}, nil
}

// TestPythonSealVectorsOpen proves the Python runtime's sealing.py output opens
// with this package: same framing, AAD bytes and encryption context.
func TestPythonSealVectorsOpen(t *testing.T) {
	data, err := os.ReadFile("../../../contracts/customer-updates/sealing-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Version int             `json:"version"`
		Vectors []sealingVector `json:"vectors"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || len(fixture.Vectors) < 2 {
		t.Fatalf("unexpected fixture: version %d, %d vectors", fixture.Version, len(fixture.Vectors))
	}
	for _, v := range fixture.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			dataKey, err1 := hex.DecodeString(v.DataKeyHex)
			wrapped, err2 := hex.DecodeString(v.WrappedKeyHex)
			nonce, err3 := hex.DecodeString(v.NonceHex)
			if err := errors.Join(err1, err2, err3); err != nil || len(nonce) != 12 {
				t.Fatalf("bad vector hex: %v", err)
			}
			e := NewEnvelope(&vectorKMS{dataKey: dataKey, wrapped: wrapped, binding: v.Binding}, "key")
			got, err := e.Open(context.Background(), v.SealedBase64, v.Binding)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if string(got) != v.PlaintextUTF8 {
				t.Fatalf("plaintext = %q, want %q", got, v.PlaintextUTF8)
			}
			other := maps.Clone(v.Binding)
			other["purpose"] += "-other"
			wrong := NewEnvelope(&vectorKMS{dataKey: dataKey, wrapped: wrapped, binding: other}, "key")
			if _, err := wrong.Open(context.Background(), v.SealedBase64, other); !errors.Is(err, ErrInvalid) {
				t.Fatalf("a value opened under another binding: %v", err)
			}
		})
	}
}
