// Package sealing encrypts sensitive record fields with KMS envelope
// encryption: a fresh AES-256 data key per record, the data key wrapped by KMS
// under an encryption context that names the record, and the context also
// bound as AES-GCM additional data. A sealed value cannot be opened for a
// different record, and plaintext data keys never leave process memory.
package sealing

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// ErrUnavailable means no key is configured; writers must fail closed.
var ErrUnavailable = errors.New("record encryption unavailable")

// ErrInvalid means a sealed value is malformed or bound to another record.
var ErrInvalid = errors.New("sealed value invalid")

const version byte = 1

type KMSAPI interface {
	GenerateDataKey(context.Context, *kms.GenerateDataKeyInput, ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error)
	Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// Sealer seals and opens values bound to an encryption context.
type Sealer interface {
	Seal(ctx context.Context, plaintext []byte, binding map[string]string) (string, error)
	Open(ctx context.Context, sealed string, binding map[string]string) ([]byte, error)
}

type Envelope struct {
	kms   KMSAPI
	keyID string
}

// NewEnvelope returns nil when keyID is empty so callers can detect the
// unconfigured state explicitly.
func NewEnvelope(client KMSAPI, keyID string) *Envelope {
	if client == nil || keyID == "" {
		return nil
	}
	return &Envelope{kms: client, keyID: keyID}
}

// aad is the canonical JSON of the binding (sorted keys) so the GCM tag and
// the KMS encryption context name the same record.
func aad(binding map[string]string) ([]byte, error) {
	if len(binding) == 0 {
		return nil, ErrInvalid
	}
	keys := make([]string, 0, len(binding))
	for k := range binding {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([][2]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, [2]string{k, binding[k]})
	}
	return json.Marshal(pairs)
}

func (e *Envelope) Seal(ctx context.Context, plaintext []byte, binding map[string]string) (string, error) {
	if e == nil {
		return "", ErrUnavailable
	}
	additional, err := aad(binding)
	if err != nil {
		return "", err
	}
	key, err := e.kms.GenerateDataKey(ctx, &kms.GenerateDataKeyInput{
		KeyId: aws.String(e.keyID), KeySpec: kmstypes.DataKeySpecAes256, EncryptionContext: binding,
	})
	if err != nil {
		return "", err
	}
	defer clear(key.Plaintext)
	if len(key.Plaintext) != 32 || len(key.CiphertextBlob) == 0 || len(key.CiphertextBlob) > 0xffff {
		return "", errors.New("unexpected KMS data key")
	}
	gcm, err := newGCM(key.Plaintext)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	var out bytes.Buffer
	out.WriteByte(version)
	_ = binary.Write(&out, binary.BigEndian, uint16(len(key.CiphertextBlob)))
	out.Write(key.CiphertextBlob)
	out.Write(nonce)
	out.Write(gcm.Seal(nil, nonce, plaintext, additional))
	return base64.StdEncoding.EncodeToString(out.Bytes()), nil
}

func (e *Envelope) Open(ctx context.Context, sealed string, binding map[string]string) ([]byte, error) {
	if e == nil {
		return nil, ErrUnavailable
	}
	additional, err := aad(binding)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(sealed)
	if err != nil || len(raw) < 3 || raw[0] != version {
		return nil, ErrInvalid
	}
	n := int(binary.BigEndian.Uint16(raw[1:3]))
	if n == 0 || len(raw) < 3+n+12+16 {
		return nil, ErrInvalid
	}
	wrapped, rest := raw[3:3+n], raw[3+n:]
	key, err := e.kms.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob: wrapped, EncryptionContext: binding, KeyId: aws.String(e.keyID),
	})
	if err != nil {
		var invalid *kmstypes.InvalidCiphertextException
		if errors.As(err, &invalid) {
			return nil, ErrInvalid
		}
		return nil, err
	}
	defer clear(key.Plaintext)
	gcm, err := newGCM(key.Plaintext)
	if err != nil {
		return nil, err
	}
	nonce, body := rest[:gcm.NonceSize()], rest[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, body, additional)
	if err != nil {
		return nil, ErrInvalid
	}
	return plain, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("unexpected data key length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
