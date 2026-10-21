package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/ttobak/backend/internal/connector"
)

type connectorPublicKeyFunc func(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)

func (f connectorPublicKeyFunc) GetPublicKey(ctx context.Context, in *kms.GetPublicKeyInput, options ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	return f(ctx, in, options...)
}

func TestConnectorPublicKeyLookupPinsIdentityAndCachesOnlyValidKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	spki, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	keyID := "arn:aws:kms:ap-northeast-2:111111111111:key/00000000-0000-0000-0000-000000000000"
	for _, mode := range []string{"correct", "foreign", "wrong usage", "failure"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			client := connectorPublicKeyFunc(func(ctx context.Context, in *kms.GetPublicKeyInput, options ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
				calls++
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 3*time.Second || aws.ToString(in.KeyId) != keyID {
					t.Fatal("lookup lost its key/deadline binding")
				}
				opts := kms.Options{}
				for _, option := range options {
					option(&opts)
				}
				if opts.Retryer == nil || opts.Retryer.MaxAttempts() != 1 {
					t.Fatal("lookup retries are unbounded")
				}
				out := &kms.GetPublicKeyOutput{KeyId: aws.String(keyID), PublicKey: spki, KeyUsage: kmstypes.KeyUsageTypeSignVerify}
				switch mode {
				case "foreign":
					out.KeyId = aws.String("foreign-key")
				case "wrong usage":
					out.KeyUsage = kmstypes.KeyUsageTypeEncryptDecrypt
				case "failure":
					return nil, errors.New("private SDK response")
				}
				return out, nil
			})
			verify := lazyConnectorVerifier(client, keyID, func(context.Context, string, string) (connector.Grants, error) {
				t.Fatal("invalid wire reached grant store")
				return connector.Grants{}, nil
			})
			for range 2 {
				_, err := verify(context.Background(), []byte("{}"), connector.Request{})
				expected := connector.ErrPolicyUnavailable
				if mode == "correct" {
					expected = connector.ErrInvalidDelegation
				}
				if !errors.Is(err, expected) {
					t.Fatalf("unexpected diagnostic: %v", err)
				}
			}
			if (mode == "correct" && calls != 1) || (mode != "correct" && calls != 2) {
				t.Fatalf("incorrect key cache: %d calls", calls)
			}
		})
	}
}

func TestConnectorPublicKeyLookupRejectsCancelledRequestBeforeAWS(t *testing.T) {
	client := connectorPublicKeyFunc(func(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
		t.Fatal("cancelled request reached AWS")
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := lazyConnectorVerifier(client, "", nil)(ctx, nil, connector.Request{})
	if !errors.Is(err, connector.ErrPolicyUnavailable) {
		t.Fatal(err)
	}
}
