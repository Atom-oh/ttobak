package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/ttobak/backend/internal/middleware"
)

type diagnosticSecretClient struct {
	output *secretsmanager.GetSecretValueOutput
	err    error
	calls  int
}

func (c *diagnosticSecretClient) GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	c.calls++
	return c.output, c.err
}

func captureAuthorizerLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	oldWriter, oldFlags, oldPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	})
	return &output
}

func TestOriginFailureLogsFixedReasonsWithoutPrivateInputs(t *testing.T) {
	secret := strings.Repeat("PrivateOrigin", 4) + "123456789012"
	token, privateError := "PRIVATE_JWT_MARKER", "PRIVATE_SDK_MESSAGE\nforged_log=secret"
	oldPool, oldToken, oldOrigin := cognitoUserPoolID, verifyToken, cloudFrontOrigin
	t.Cleanup(func() { cognitoUserPoolID, verifyToken, cloudFrontOrigin = oldPool, oldToken, oldOrigin })
	cognitoUserPoolID = "test-pool"
	for _, test := range []struct {
		name, reason, category string
		headers                map[string]string
		multi                  map[string][]string
		err                    error
		output                 *secretsmanager.GetSecretValueOutput
		unconfigured, canceled bool
		calls                  int
	}{
		{name: "missing", reason: "header_missing", category: "none"},
		{name: "empty", reason: "header_malformed", category: "none", headers: map[string]string{originHeaderName: ""}},
		{name: "short", reason: "header_malformed", category: "none", headers: map[string]string{originHeaderName: "PRIVATE_SHORT_HEADER"}},
		{name: "duplicate-case", reason: "header_duplicate", category: "none", headers: map[string]string{originHeaderName: secret, "X-Origin-Verify": secret}},
		{name: "multi-empty", reason: "header_malformed", category: "none", multi: map[string][]string{originHeaderName: {}}},
		{name: "multi-duplicate", reason: "header_duplicate", category: "none", multi: map[string][]string{originHeaderName: {secret, secret}}},
		{name: "multi-duplicate-case", reason: "header_duplicate", category: "none", multi: map[string][]string{originHeaderName: {secret}, "X-Origin-Verify": {secret}}},
		{name: "conflict", reason: "header_conflict", category: "none", headers: map[string]string{originHeaderName: secret}, multi: map[string][]string{originHeaderName: {strings.Repeat("b", 64)}}},
		{name: "mismatch", reason: "mismatch", category: "none", headers: map[string]string{originHeaderName: strings.Repeat("b", 64)}, calls: 1},
		{name: "unconfigured", reason: "unconfigured", category: "none", unconfigured: true},
		{name: "canceled-before-read", reason: "secret_unavailable", category: "canceled", canceled: true},
		{name: "deadline", reason: "secret_unavailable", category: "deadline", err: fmt.Errorf("%s: %w", privateError, context.DeadlineExceeded), calls: 1},
		{name: "canceled-read", reason: "secret_unavailable", category: "canceled", err: context.Canceled, calls: 1},
		{name: "transport", reason: "secret_unavailable", category: "transport", err: &smithyhttp.RequestSendError{Err: errors.New(privateError)}, calls: 1},
		{name: "unknown-error", reason: "secret_unavailable", category: "other", err: errors.New(privateError), calls: 1},
		{name: "unknown-aws-code", reason: "secret_unavailable", category: "aws_other", err: &smithy.GenericAPIError{Code: privateError, Message: privateError}, calls: 1},
		{name: "nil-secret", reason: "secret_unavailable", category: "invalid_secret", output: &secretsmanager.GetSecretValueOutput{}, calls: 1},
		{name: "malformed-secret", reason: "secret_unavailable", category: "invalid_secret", output: &secretsmanager.GetSecretValueOutput{SecretString: aws.String(secret[:63] + "\n")}, calls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := captureAuthorizerLog(t)
			client := &diagnosticSecretClient{output: &secretsmanager.GetSecretValueOutput{SecretString: &secret}, err: test.err}
			if test.output != nil {
				client.output = test.output
			}
			cloudFrontOrigin = newOriginVerifier(testSecretARN, client)
			if test.unconfigured {
				cloudFrontOrigin.secretARN = ""
			}
			event := proxyEvent(secret)
			event.QueryStringParameters["token"] = token
			if test.headers != nil || test.multi != nil || test.name == "missing" {
				event.Headers, event.MultiValueHeaders = test.headers, test.multi
			}
			tokenCalls := 0
			verifyToken = func(string) (*middleware.ALBOIDCClaims, error) {
				tokenCalls++
				return &middleware.ALBOIDCClaims{Sub: "synthetic-user"}, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.canceled {
				cancel()
			}
			response, err := handler(ctx, event)
			if err != nil || response.PolicyDocument.Statement[0].Effect != "Deny" || tokenCalls != 0 || client.calls != test.calls {
				t.Fatalf("auth decision/call order changed: err=%v tokenCalls=%d secretCalls=%d", err, tokenCalls, client.calls)
			}
			want := fmt.Sprintf("ws-authorizer: origin verification rejected reason=%s category=%s\n", test.reason, test.category)
			if output.String() != want {
				t.Errorf("expected fixed diagnostic %q, got %q", want, output.String())
			}
			for _, forbidden := range []string{secret, token, testSecretARN, event.MethodArn, privateError, "PRIVATE_SHORT_HEADER", strings.Repeat("b", 64)} {
				if strings.Contains(output.String(), forbidden) {
					t.Error("private input appeared in diagnostic log")
				}
			}
		})
	}
}

func TestOriginAWSFailureLogCategoryAllowlist(t *testing.T) {
	oldPool, oldToken, oldOrigin := cognitoUserPoolID, verifyToken, cloudFrontOrigin
	t.Cleanup(func() { cognitoUserPoolID, verifyToken, cloudFrontOrigin = oldPool, oldToken, oldOrigin })
	cognitoUserPoolID = "test-pool"
	verifyToken = func(string) (*middleware.ALBOIDCClaims, error) {
		t.Fatal("JWT must not run after origin read failure")
		return nil, nil
	}
	for _, test := range []struct{ code, category string }{
		{"AccessDeniedException", "access_denied"}, {"ResourceNotFoundException", "not_found"},
		{"ThrottlingException", "throttled"}, {"TooManyRequestsException", "throttled"},
		{"InternalServiceError", "service_error"}, {"ServiceUnavailableException", "service_error"},
		{"DecryptionFailure", "decryption_failed"}, {"InvalidRequestException", "invalid_request"},
		{"InvalidParameterException", "invalid_request"}, {"ExpiredTokenException", "credentials"},
		{"UnrecognizedClientException", "credentials"}, {"InvalidSignatureException", "credentials"},
	} {
		t.Run(test.code, func(t *testing.T) {
			output := captureAuthorizerLog(t)
			private := testSecretARN + " PRIVATE_SERVICE_DETAIL"
			client := &diagnosticSecretClient{err: fmt.Errorf("%s: %w", private,
				&smithy.GenericAPIError{Code: test.code, Message: private})}
			cloudFrontOrigin = newOriginVerifier(testSecretARN, client)
			response, err := handler(context.Background(), proxyEvent(strings.Repeat("a", 64)))
			if err != nil || response.PolicyDocument.Statement[0].Effect != "Deny" || client.calls != 1 {
				t.Fatal("service failure must deny after exactly one read")
			}
			want := "ws-authorizer: origin verification rejected reason=secret_unavailable category=" + test.category + "\n"
			if output.String() != want {
				t.Errorf("expected allowlisted diagnostic %q, got %q", want, output.String())
			}
		})
	}
}

func TestExpiredSecretFailureIsLoggedWithoutFallback(t *testing.T) {
	secret := strings.Repeat("a", 64)
	client := &diagnosticSecretClient{output: &secretsmanager.GetSecretValueOutput{SecretString: &secret}}
	oldPool, oldToken, oldOrigin := cognitoUserPoolID, verifyToken, cloudFrontOrigin
	t.Cleanup(func() { cognitoUserPoolID, verifyToken, cloudFrontOrigin = oldPool, oldToken, oldOrigin })
	cognitoUserPoolID = "test-pool"
	verifyToken = func(string) (*middleware.ALBOIDCClaims, error) {
		return &middleware.ALBOIDCClaims{Sub: "synthetic-user"}, nil
	}
	cloudFrontOrigin = newOriginVerifier(testSecretARN, client)
	now := time.Unix(1000, 0)
	cloudFrontOrigin.now = func() time.Time { return now }
	output := captureAuthorizerLog(t)
	event := proxyEvent(secret)
	for i := 0; i < 2; i++ {
		response, _ := handler(context.Background(), event)
		if response.PolicyDocument.Statement[0].Effect != "Allow" || output.Len() != 0 || client.calls != 1 {
			t.Fatal("valid cached request changed or logged private input")
		}
	}
	now = now.Add(time.Minute + time.Second)
	client.err = &smithy.GenericAPIError{Code: "InternalServiceError", Message: "PRIVATE_ERROR"}
	response, _ := handler(context.Background(), event)
	if response.PolicyDocument.Statement[0].Effect != "Deny" || client.calls != 2 {
		t.Fatal("expired cache must not grant access or retry the failed read")
	}
	if output.String() != "ws-authorizer: origin verification rejected reason=secret_unavailable category=service_error\n" {
		t.Errorf("missing fixed refresh-failure diagnostic: %q", output.String())
	}
}

func TestOriginDiagnosticDefaultsCannotAllowAccess(t *testing.T) {
	for _, result := range []originResult{{}, {reason: originReason(255)},
		{reason: originAccepted, category: secretErrorOther}} {
		if result.allowed() {
			t.Fatal("incomplete or invalid result allowed access")
		}
	}
	if originReason(255).String() != "unknown" || secretErrorCategory(255).String() != "other" {
		t.Fatal("invalid enum did not use a fixed diagnostic label")
	}
}
