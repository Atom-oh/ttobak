package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/ttobak/backend/internal/connector"
)

var connectorBridge *connector.Bridge

type publicKeyReader interface {
	GetPublicKey(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
}

func lazyConnectorVerifier(client publicKeyReader, keyARN string, resolve connector.GrantResolver) func(context.Context, []byte, connector.Request) (connector.Principal, error) {
	lookup := make(chan struct{}, 1)
	var verifier *connector.Verifier
	return func(ctx context.Context, wire []byte, request connector.Request) (connector.Principal, error) {
		readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		select {
		case lookup <- struct{}{}:
		case <-readCtx.Done():
			return connector.Principal{}, connector.ErrPolicyUnavailable
		}
		if readCtx.Err() != nil {
			<-lookup
			return connector.Principal{}, connector.ErrPolicyUnavailable
		}
		if verifier == nil {
			if client == nil {
				<-lookup
				return connector.Principal{}, connector.ErrPolicyUnavailable
			}
			key, err := client.GetPublicKey(readCtx, &kms.GetPublicKeyInput{KeyId: aws.String(keyARN)},
				func(options *kms.Options) { options.Retryer = aws.NopRetryer{} })
			if err != nil || key == nil || aws.ToString(key.KeyId) != keyARN ||
				key.KeyUsage != kmstypes.KeyUsageTypeSignVerify {
				<-lookup
				return connector.Principal{}, connector.ErrPolicyUnavailable
			}
			var errConfig error
			verifier, errConfig = connector.NewVerifier(keyARN, key.PublicKey, "ttobak-api-bridge", resolve, nil)
			if errConfig != nil {
				<-lookup
				return connector.Principal{}, errConfig
			}
		}
		current := verifier
		<-lookup
		return current.Verify(ctx, wire, request)
	}
}

// API Gateway supplies its own event root; caller bodies cannot supply this
// top-level private protocol. Direct Lambda calls require service IAM Invoke.
func dispatchAPI(ctx context.Context, raw json.RawMessage) (any, error) {
	if connector.IsInvokeFrame(raw) {
		return connectorBridge.Invoke(ctx, raw), nil
	}
	var event events.APIGatewayProxyRequest
	if err := json.Unmarshal(raw, &event); err != nil {
		return nil, err
	}
	return chiLambda.ProxyWithContext(ctx, event)
}
