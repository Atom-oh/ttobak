package service

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

var ErrCalendarEncryption = errors.New("calendar encryption unavailable")

type CalendarKMSClient interface {
	Encrypt(context.Context, *kms.EncryptInput, ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

type CalendarCipher struct {
	client CalendarKMSClient
	keyID  string
}

func NewCalendarCipher(client CalendarKMSClient, keyID string) *CalendarCipher {
	return &CalendarCipher{client: client, keyID: keyID}
}

func (crypto *CalendarCipher) Encrypt(ctx context.Context, userID, sourceID, eventID string, plaintext []byte) (string, error) {
	if !crypto.configured() || userID == "" || sourceID == "" || eventID == "" || len(plaintext) == 0 || len(plaintext) > 4096 {
		return "", ErrCalendarEncryption
	}
	result, err := crypto.client.Encrypt(ctx, &kms.EncryptInput{
		KeyId: aws.String(crypto.keyID), Plaintext: plaintext,
		EncryptionContext: calendarEncryptionContext(userID, sourceID, eventID),
	})
	if err != nil || result == nil || len(result.CiphertextBlob) == 0 || len(result.CiphertextBlob) > 6144 {
		return "", ErrCalendarEncryption
	}
	return base64.StdEncoding.EncodeToString(result.CiphertextBlob), nil
}

func (crypto *CalendarCipher) Decrypt(ctx context.Context, userID, sourceID, eventID string, ciphertext string) ([]byte, error) {
	if !crypto.configured() || userID == "" || sourceID == "" || eventID == "" || len(ciphertext) == 0 || len(ciphertext) > 8192 {
		return nil, ErrCalendarEncryption
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(ciphertext)
	if err != nil || len(blob) == 0 || len(blob) > 6144 {
		return nil, ErrCalendarEncryption
	}
	result, err := crypto.client.Decrypt(ctx, &kms.DecryptInput{
		KeyId: aws.String(crypto.keyID), CiphertextBlob: blob,
		EncryptionContext: calendarEncryptionContext(userID, sourceID, eventID),
	})
	if err != nil || result == nil || len(result.Plaintext) == 0 || len(result.Plaintext) > 4096 {
		return nil, ErrCalendarEncryption
	}
	return result.Plaintext, nil
}

func (crypto *CalendarCipher) configured() bool {
	return crypto != nil && crypto.client != nil && strings.TrimSpace(crypto.keyID) != ""
}

func calendarEncryptionContext(userID, sourceID, eventID string) map[string]string {
	return map[string]string{
		"purpose": "ttobak-calendar-event-v1",
		"userId":  userID, "sourceId": sourceID, "eventId": eventID,
	}
}
