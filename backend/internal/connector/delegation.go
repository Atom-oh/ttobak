package connector

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"time"
	"unicode/utf8"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var keyARN = regexp.MustCompile(`^arn:(aws|aws-us-gov|aws-cn):kms:[a-z0-9-]{1,32}:[0-9]{12}:key/[0-9a-f-]{36}$`)

type envelope struct {
	KeyID     string `json:"keyId"`
	Algorithm string `json:"algorithm"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type Request struct {
	Operation string
	RequestID string
	Arguments []byte
}

type Verifier struct {
	keyID, destination string
	publicKey          *rsa.PublicKey
	resolve            GrantResolver
	clock              func() int64
}

func (v *Verifier) currentGrants(ctx context.Context, user, client string) (grants Grants, err error) {
	defer func() {
		if recover() != nil {
			grants, err = Grants{}, ErrPolicyUnavailable
		}
	}()
	return v.resolve(ctx, user, client)
}

// NewVerifier accepts one pinned KMS key ARN and its SPKI DER public key.
// No network lookup, key URL, alias resolution or caller algorithm is accepted.
func NewVerifier(keyID string, spki []byte, destination string, resolve GrantResolver, clock func() int64) (*Verifier, error) {
	if !keyARN.MatchString(keyID) || len(spki) > 2048 || resolve == nil ||
		(destination != "ttobak-api-bridge" && destination != "ttobak-qa-bridge") {
		return nil, ErrInvalidConfig
	}
	key, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, ErrInvalidConfig
	}
	pub, ok := key.(*rsa.PublicKey)
	if !ok || (pub.N.BitLen() != 2048 && pub.N.BitLen() != 3072 && pub.N.BitLen() != 4096) || pub.E != 65537 {
		return nil, ErrInvalidConfig
	}
	if clock == nil {
		clock = func() int64 { return time.Now().Unix() }
	}
	return &Verifier{keyID, destination, pub, resolve, clock}, nil
}

func canonicalJSON(raw []byte, target any) bool {
	if !utf8.Valid(raw) {
		return false
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return false
	}
	canonical, err := json.Marshal(target)
	return err == nil && bytes.Equal(raw, canonical)
}

func decodeBase64(s string, limit int) ([]byte, bool) {
	if len(s) > (limit*4+2)/3 {
		return nil, false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return b, err == nil && len(b) <= limit && base64.RawURLEncoding.EncodeToString(b) == s
}

func validPayload(p payload) bool {
	digest, err := hex.DecodeString(p.ArgumentsSHA256)
	return p.Version == 1 && p.Protocol == Protocol && identifier.MatchString(p.Sub) &&
		identifier.MatchString(p.ClientID) && identifier.MatchString(p.RequestID) &&
		validCapabilities(p.Capabilities) && p.ApprovalGeneration >= 0 && p.ApprovalGeneration <= maxSafeInteger &&
		err == nil && len(digest) == sha256.Size && hex.EncodeToString(digest) == p.ArgumentsSHA256 &&
		p.IssuedAt > 0 && p.ExpiresAt > p.IssuedAt && p.UpstreamExpiresAt >= p.ExpiresAt &&
		p.UpstreamExpiresAt <= maxSafeInteger && p.ExpiresAt-p.IssuedAt <= MaxLifetimeSeconds
}

// SigningDigest returns the exact SHA-256 bytes to pass to KMS Sign with
// MessageType=DIGEST and RSASSA_PSS_SHA_256, not a hash of this hash.
func SigningDigest(rawPayload []byte) ([]byte, error) {
	var p payload
	if len(rawPayload) > MaxPayloadBytes || !canonicalJSON(rawPayload, &p) || !validPayload(p) {
		return nil, ErrInvalidDelegation
	}
	digest := sha256.Sum256(rawPayload)
	return digest[:], nil
}

func (v *Verifier) Verify(ctx context.Context, wire []byte, request Request) (Principal, error) {
	empty := Principal{}
	now := v.clock()
	if len(wire) == 0 || len(wire) > MaxWireBytes || len(request.Arguments) > MaxArgumentBytes ||
		!utf8.Valid(request.Arguments) || !identifier.MatchString(request.RequestID) || now <= 0 || now > maxSafeInteger {
		return empty, ErrInvalidDelegation
	}
	var e envelope
	if !canonicalJSON(wire, &e) {
		return empty, ErrInvalidDelegation
	}
	if e.KeyID != v.keyID {
		return empty, ErrUntrustedKey
	}
	if e.Algorithm != SigningAlgorithm {
		return empty, ErrInvalidDelegation
	}
	raw, ok := decodeBase64(e.Payload, MaxPayloadBytes)
	if !ok {
		return empty, ErrInvalidDelegation
	}
	signature, ok := decodeBase64(e.Signature, 512)
	if !ok || len(signature) != v.publicKey.Size() {
		return empty, ErrInvalidSignature
	}
	digest := sha256.Sum256(raw)
	if err := rsa.VerifyPSS(v.publicKey, crypto.SHA256, digest[:], signature,
		&rsa.PSSOptions{SaltLength: sha256.Size, Hash: crypto.SHA256}); err != nil {
		return empty, ErrInvalidSignature
	}
	var p payload
	if !canonicalJSON(raw, &p) || !validPayload(p) {
		return empty, ErrInvalidDelegation
	}
	if p.Destination != v.destination || p.Operation != request.Operation || p.RequestID != request.RequestID {
		return empty, ErrRequestMismatch
	}
	argDigest := sha256.Sum256(request.Arguments)
	expected, _ := hex.DecodeString(p.ArgumentsSHA256)
	if subtle.ConstantTimeCompare(expected, argDigest[:]) != 1 {
		return empty, ErrRequestMismatch
	}
	if p.ExpiresAt <= now || p.IssuedAt > now+5 {
		return empty, ErrExpired
	}
	required, err := RequiredCapabilities(p.Operation, v.destination)
	if err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, ErrPolicyUnavailable
	}
	grants, err := v.currentGrants(ctx, p.Sub, p.ClientID)
	if err != nil || ctx.Err() != nil {
		return empty, ErrPolicyUnavailable
	}
	now = v.clock()
	if now <= 0 || now > maxSafeInteger || p.ExpiresAt <= now || p.IssuedAt > now+5 {
		return empty, ErrExpired
	}
	caps, err := effectiveCapabilities(p, grants, now)
	if err != nil {
		return empty, err
	}
	principal := Principal{p.Sub, p.ClientID, p.Operation, p.RequestID, caps, p.ApprovalGeneration, p.ExpiresAt}
	for _, cap := range required {
		found := false
		for _, have := range caps {
			if cap == have {
				found = true
				break
			}
		}
		if !found {
			return empty, ErrCapabilityDenied
		}
	}
	return principal, nil
}
