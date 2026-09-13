package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

const originHeaderName = "x-origin-verify"

type secretValueClient interface {
	GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

type originVerifier struct {
	secretARN string
	client    secretValueClient
	now       func() time.Time
	mu        sync.Mutex
	value     string
	expires   time.Time
}

func newOriginVerifier(arn string, client secretValueClient) *originVerifier {
	return &originVerifier{secretARN: arn, client: client, now: time.Now}
}

func (v *originVerifier) verify(ctx context.Context, event events.APIGatewayCustomAuthorizerRequestTypeRequest) bool {
	if v == nil || v.secretARN == "" || v.client == nil {
		return false
	}
	header, ok := originHeader(event)
	if !ok || len(header) != 64 {
		return false
	}
	expected, err := v.secret(ctx)
	return err == nil && subtle.ConstantTimeCompare([]byte(header), []byte(expected)) == 1
}

func (v *originVerifier) secret(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if v.value != "" && v.now().Before(v.expires) {
		return v.value, nil
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := v.client.GetSecretValue(bounded, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(v.secretARN),
	}, func(options *secretsmanager.Options) { options.Retryer = aws.NopRetryer{} })
	if err != nil || out == nil || out.SecretString == nil || !validOriginSecret(*out.SecretString) {
		// Never reuse an expired secret or expose a service response in an auth log.
		return "", errors.New("origin secret unavailable")
	}
	v.value = *out.SecretString
	v.expires = v.now().Add(time.Minute)
	return v.value, nil
}

func validOriginSecret(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func originHeader(event events.APIGatewayCustomAuthorizerRequestTypeRequest) (string, bool) {
	value, found := "", false
	for name, header := range event.Headers {
		if strings.EqualFold(name, originHeaderName) {
			if found {
				return "", false
			}
			value, found = header, true
		}
	}
	multiFound := false
	for name, headers := range event.MultiValueHeaders {
		if strings.EqualFold(name, originHeaderName) {
			if multiFound || len(headers) != 1 || (found && headers[0] != value) {
				return "", false
			}
			value, found, multiFound = headers[0], true, true
		}
	}
	return value, found && value != ""
}
