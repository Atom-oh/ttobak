package service

import (
	"context"
	"encoding/base64"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// CryptoService provides KMS-based encryption for sensitive data
type CryptoService struct {
	kmsClient *kms.Client
	keyID     string
}

// NewCryptoService creates a new crypto service
func NewCryptoService(kmsClient *kms.Client, keyID string) *CryptoService {
	return &CryptoService{
		kmsClient: kmsClient,
		keyID:     keyID,
	}
}

// Encrypt encrypts plaintext using KMS and returns base64-encoded ciphertext
func (s *CryptoService) Encrypt(ctx context.Context, plaintext string) (string, error) {
	result, err := s.kmsClient.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     aws.String(s.keyID),
		Plaintext: []byte(plaintext),
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(result.CiphertextBlob), nil
}

// Decrypt decrypts base64-encoded ciphertext using KMS
func (s *CryptoService) Decrypt(ctx context.Context, ciphertext string) (string, error) {
	blob, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	result, err := s.kmsClient.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob: blob,
	})
	if err != nil {
		return "", err
	}
	return string(result.Plaintext), nil
}
