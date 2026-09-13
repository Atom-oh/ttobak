package main

import (
	"context"
	"crypto/subtle"
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

func (v *originVerifier) verify(ctx context.Context, event events.APIGatewayCustomAuthorizerRequestTypeRequest) originResult {
	if v == nil || v.secretARN == "" || v.client == nil {
		return originResult{reason: originUnconfigured}
	}
	header, reason := originHeader(event)
	if reason != originAccepted {
		return originResult{reason: reason}
	}
	if len(header) != 64 {
		return originResult{reason: originHeaderMalformed}
	}
	expected, category := v.secret(ctx)
	if category != secretErrorNone {
		return originResult{reason: originSecretUnavailable, category: category}
	}
	if subtle.ConstantTimeCompare([]byte(header), []byte(expected)) != 1 {
		return originResult{reason: originMismatch}
	}
	return originResult{reason: originAccepted}
}

func (v *originVerifier) secret(ctx context.Context) (string, secretErrorCategory) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", classifySecretError(err)
	}
	if v.value != "" && v.now().Before(v.expires) {
		return v.value, secretErrorNone
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := v.client.GetSecretValue(bounded, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(v.secretARN),
	}, func(options *secretsmanager.Options) { options.Retryer = aws.NopRetryer{} })
	if err != nil {
		return "", classifySecretError(err)
	}
	if out == nil || out.SecretString == nil || !validOriginSecret(*out.SecretString) {
		// Never reuse an expired secret or expose a service response in an auth log.
		return "", secretErrorInvalidSecret
	}
	v.value = *out.SecretString
	v.expires = v.now().Add(time.Minute)
	return v.value, secretErrorNone
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

func originHeader(event events.APIGatewayCustomAuthorizerRequestTypeRequest) (string, originReason) {
	value, found := "", false
	for name, header := range event.Headers {
		if strings.EqualFold(name, originHeaderName) {
			if found {
				return "", originHeaderDuplicate
			}
			value, found = header, true
		}
	}
	multiFound := false
	for name, headers := range event.MultiValueHeaders {
		if strings.EqualFold(name, originHeaderName) {
			if multiFound || len(headers) > 1 {
				return "", originHeaderDuplicate
			}
			if len(headers) == 0 {
				return "", originHeaderMalformed
			}
			if found && headers[0] != value {
				return "", originHeaderConflict
			}
			value, found, multiFound = headers[0], true, true
		}
	}
	if !found {
		return "", originHeaderMissing
	}
	return value, originAccepted
}
