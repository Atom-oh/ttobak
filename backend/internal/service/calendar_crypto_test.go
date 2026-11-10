package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

type calendarKMSFake struct {
	encryptCalls int
	decryptCalls int
	failure      error
	plaintexts   [][]byte
	contexts     []map[string]string
}

func calendarTestAEAD() cipher.AEAD {
	block, err := aes.NewCipher(bytes.Repeat([]byte{19}, 32))
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return aead
}

func (fake *calendarKMSFake) Encrypt(_ context.Context, input *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	fake.encryptCalls++
	if fake.failure != nil {
		return nil, fake.failure
	}
	if aws.ToString(input.KeyId) != "configured-key" {
		return nil, errors.New("wrong key")
	}
	fake.plaintexts = append(fake.plaintexts, append([]byte(nil), input.Plaintext...))
	fake.contexts = append(fake.contexts, input.EncryptionContext)
	aead := calendarTestAEAD()
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	additional, _ := json.Marshal(input.EncryptionContext)
	return &kms.EncryptOutput{CiphertextBlob: aead.Seal(nonce, nonce, input.Plaintext, additional)}, nil
}

func (fake *calendarKMSFake) Decrypt(_ context.Context, input *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	fake.decryptCalls++
	if fake.failure != nil {
		return nil, fake.failure
	}
	if aws.ToString(input.KeyId) != "configured-key" {
		return nil, errors.New("wrong key")
	}
	aead := calendarTestAEAD()
	if len(input.CiphertextBlob) < aead.NonceSize() {
		return nil, errors.New("invalid ciphertext")
	}
	additional, _ := json.Marshal(input.EncryptionContext)
	plain, err := aead.Open(nil, input.CiphertextBlob[:aead.NonceSize()], input.CiphertextBlob[aead.NonceSize():], additional)
	if err != nil {
		return nil, err
	}
	return &kms.DecryptOutput{Plaintext: plain}, nil
}

func TestCalendarCipherBindsContextAndConfiguredKey(test *testing.T) {
	sdk := &calendarKMSFake{}
	crypto := NewCalendarCipher(sdk, "configured-key")
	plaintext := []byte(`{"title":"Private synthetic title"}`)
	ciphertext, err := crypto.Encrypt(context.Background(), "owner", "source", "event", plaintext)
	if err != nil || ciphertext == "" || strings.Contains(ciphertext, "Private") {
		test.Fatalf("encryption failed: %v", err)
	}
	expected := map[string]string{"purpose": "ttobak-calendar-event-v1", "userId": "owner", "sourceId": "source", "eventId": "event"}
	if !reflect.DeepEqual(sdk.contexts[0], expected) {
		test.Fatalf("context contains missing or sensitive fields: %v", sdk.contexts[0])
	}
	for _, scope := range [][3]string{{"owner", "source", "event"}, {"other", "source", "event"}, {"owner", "other", "event"}, {"owner", "source", "other"}} {
		test.Run(strings.Join(scope[:], "/"), func(test *testing.T) {
			result, err := crypto.Decrypt(context.Background(), scope[0], scope[1], scope[2], ciphertext)
			if scope == [3]string{"owner", "source", "event"} {
				if err != nil || !bytes.Equal(result, plaintext) {
					test.Fatalf("matching context cannot decrypt: %v", err)
				}
			} else if !errors.Is(err, ErrCalendarEncryption) || result != nil {
				test.Fatal("foreign encryption context returned private text")
			}
		})
	}
	additional, _ := json.Marshal(map[string]string{"purpose": "other", "userId": "owner", "sourceId": "source", "eventId": "event"})
	aead := calendarTestAEAD()
	nonce := make([]byte, aead.NonceSize())
	foreign := base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, plaintext, additional))
	if result, err := crypto.Decrypt(context.Background(), "owner", "source", "event", foreign); err == nil || result != nil {
		test.Fatal("another purpose must not decrypt")
	}
}

func TestCalendarCipherFailsClosedAndBoundsPlaintext(test *testing.T) {
	for _, scenario := range []string{"missing key", "nil client", "kms failure", "empty", "4096 bytes", "4097 bytes"} {
		test.Run(scenario, func(test *testing.T) {
			sdk := &calendarKMSFake{}
			crypto := NewCalendarCipher(sdk, "configured-key")
			plaintext := bytes.Repeat([]byte("x"), 4096)
			switch scenario {
			case "missing key":
				crypto = NewCalendarCipher(sdk, "")
			case "nil client":
				crypto = NewCalendarCipher(nil, "configured-key")
			case "kms failure":
				sdk.failure = errors.New("private provider detail")
			case "empty":
				plaintext = nil
			case "4097 bytes":
				plaintext = append(plaintext, 'x')
			}
			result, err := crypto.Encrypt(context.Background(), "owner", "source", "event", plaintext)
			if scenario == "4096 bytes" {
				if err != nil || result == "" {
					test.Fatalf("boundary rejected: %v", err)
				}
			} else if !errors.Is(err, ErrCalendarEncryption) || result != "" || strings.Contains(err.Error(), "private provider detail") {
				test.Fatal("encryption did not fail closed with a redacted error")
			}
			if scenario != "4096 bytes" && scenario != "kms failure" && sdk.encryptCalls != 0 {
				test.Fatal("invalid encryption input reached KMS")
			}
		})
	}
	for _, ciphertext := range []string{"", "plaintext", strings.Repeat("A", 16385)} {
		sdk := &calendarKMSFake{}
		crypto := NewCalendarCipher(sdk, "configured-key")
		if result, err := crypto.Decrypt(context.Background(), "owner", "source", "event", ciphertext); err == nil || result != nil {
			test.Fatal("invalid stored ciphertext must fail closed")
		}
	}
}

func TestSealTaskText_UsesTaskPurpose(test *testing.T) {
	sdk := &calendarKMSFake{}
	crypto := NewCalendarCipher(sdk, "configured-key")
	plaintext := []byte("Follow up with the customer")
	sealed, err := crypto.SealTaskText(context.Background(), "owner", "source", "event", plaintext)
	if err != nil || sealed == "" {
		test.Fatalf("seal failed: %v", err)
	}
	expected := map[string]string{"purpose": "ttobak-specreq-task-v1", "userId": "owner", "sourceId": "source", "eventId": "event"}
	if !reflect.DeepEqual(sdk.contexts[0], expected) {
		test.Fatalf("unexpected context: %v", sdk.contexts[0])
	}
	opened, err := crypto.OpenTaskText(context.Background(), "owner", "source", "event", sealed)
	if err != nil || !bytes.Equal(opened, plaintext) {
		test.Fatalf("task text did not round-trip: %v", err)
	}
	eventCiphertext, err := crypto.Encrypt(context.Background(), "owner", "source", "event", plaintext)
	if err != nil {
		test.Fatal(err)
	}
	if result, err := crypto.OpenTaskText(context.Background(), "owner", "source", "event", eventCiphertext); !errors.Is(err, ErrCalendarEncryption) || result != nil {
		test.Fatal("an event ciphertext must not open as task text")
	}
	if result, err := crypto.Decrypt(context.Background(), "owner", "source", "event", sealed); !errors.Is(err, ErrCalendarEncryption) || result != nil {
		test.Fatal("task text must not open as an event ciphertext")
	}
	if _, err := crypto.SealTaskText(context.Background(), "owner", "source", "event", nil); !errors.Is(err, ErrCalendarEncryption) {
		test.Fatal("empty task text must be rejected")
	}
}

func TestCalendarCipher_EventPurposeUnchanged(test *testing.T) {
	sdk := &calendarKMSFake{}
	crypto := NewCalendarCipher(sdk, "configured-key")
	if _, err := crypto.Encrypt(context.Background(), "owner", "source", "event", []byte("x")); err != nil {
		test.Fatal(err)
	}
	if sdk.contexts[0]["purpose"] != "ttobak-calendar-event-v1" {
		test.Fatalf("event purpose changed: %v", sdk.contexts[0])
	}
}
