package service

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// Fixed-name SSM parameters holding the CloudFront signing key material
// (ADR-027). The private key is created manually out-of-band; the key-pair-id
// is written by TtobakFrontendStack. Literal names, not env vars, so no CDK
// stack has to reference FrontendStack.
const (
	cfSigningKeyParam = "/ttobak/cloudfront/signing-key"
	cfKeyPairIDParam  = "/ttobak/cloudfront/key-pair-id"
)

// CloudFrontSigner mints CloudFront-signed download URLs on the site domain
// (https://{domain}/media/{s3Key}) so browsers never see the raw S3 bucket
// address. It replaces the internals of UploadService's GET presigns; the
// capability-URL semantics (anyone with the URL can fetch until expiry) are
// identical to the S3 presigns it replaces.
type CloudFrontSigner struct {
	mediaBaseURL string // e.g. https://ttobak.atomai.click/media (no trailing slash)
	signer       *sign.URLSigner
}

// NewCloudFrontSigner loads the private key and key-pair-id from SSM. Any
// failure returns an error; the caller logs it and keeps the S3-presign
// fallback (local dev, or a deploy racing ahead of FrontendStack).
func NewCloudFrontSigner(ctx context.Context, ssmClient *ssm.Client, mediaBaseURL string) (*CloudFrontSigner, error) {
	keyPairID, err := getSSMParam(ctx, ssmClient, cfKeyPairIDParam)
	if err != nil {
		return nil, err
	}
	keyPEM, err := getSSMParam(ctx, ssmClient, cfSigningKeyParam)
	if err != nil {
		return nil, err
	}
	privKey, err := parseRSAPrivateKey([]byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", cfSigningKeyParam, err)
	}
	return newCloudFrontSignerWithKey(mediaBaseURL, keyPairID, privKey), nil
}

// newCloudFrontSignerWithKey is the SSM-free constructor, split out for tests.
func newCloudFrontSignerWithKey(mediaBaseURL, keyPairID string, privKey *rsa.PrivateKey) *CloudFrontSigner {
	return &CloudFrontSigner{
		mediaBaseURL: strings.TrimRight(mediaBaseURL, "/"),
		signer:       sign.NewURLSigner(keyPairID, privKey),
	}
}

// SignedURL returns a CloudFront-signed (canned policy) URL for the given S3
// key, valid for ttl. Key segments are path-escaped — keys embed sanitized
// user filenames, and the signature covers the escaped form CloudFront sees.
func (c *CloudFrontSigner) SignedURL(s3Key string, ttl time.Duration) (string, error) {
	segments := strings.Split(s3Key, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	rawURL := c.mediaBaseURL + "/" + strings.Join(segments, "/")
	signedURL, err := c.signer.Sign(rawURL, time.Now().Add(ttl))
	if err != nil {
		return "", fmt.Errorf("failed to sign CloudFront URL: %w", err)
	}
	return signedURL, nil
}

func getSSMParam(ctx context.Context, client *ssm.Client, name string) (string, error) {
	out, err := client.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("get SSM parameter %s: %w", name, err)
	}
	return aws.ToString(out.Parameter.Value), nil
}

func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("not a PKCS1 or PKCS8 RSA key: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PKCS8 key is not RSA")
	}
	return rsaKey, nil
}
