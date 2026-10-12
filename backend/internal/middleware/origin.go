package middleware

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

const originCacheTTL = time.Minute
const originMaxOverlapSeconds = int64(24 * 60 * 60)

var originGeneration = regexp.MustCompile(`^[0-9a-f]{32}$`)

type originSecretClient interface {
	GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

type originProofSet struct {
	SchemaVersion     int    `json:"schemaVersion"`
	Current           string `json:"current"`
	Previous          string `json:"previous"`
	RotationStartedAt int64  `json:"rotationStartedAt"`
	PreviousExpiresAt int64  `json:"previousExpiresAt"`
	Generation        string `json:"generation"`
	Phase             string `json:"phase"`
}

// OriginVerifier reads only the configured secret. Its cache contains proofs,
// never authentication decisions; expiry is checked on every request.
type OriginVerifier struct {
	arn, legacy string
	client      originSecretClient
	local       bool
	now         func() time.Time
	mu          sync.RWMutex
	proofs      originProofSet
	expires     time.Time
	lookup      chan struct{}
}

// legacy is temporary rollout compatibility: ARN configuration always wins,
// including when its read fails. The migration removes legacy Lambda env values.
func NewOriginVerifier(arn, legacy string, client originSecretClient, localDevelopment bool) *OriginVerifier {
	return &OriginVerifier{arn: arn, legacy: legacy, client: client, local: localDevelopment,
		now: time.Now, lookup: make(chan struct{}, 1)}
}

func validOriginProof(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if c < 33 || c > 126 || c == ',' {
			return false
		}
	}
	return true
}

func parseOriginProofSet(raw string) (originProofSet, bool) {
	var value originProofSet
	var fields map[string]json.RawMessage
	if len(raw) > 2048 || json.Unmarshal([]byte(raw), &fields) != nil {
		return value, false
	}
	for _, name := range []string{"schemaVersion", "current", "previous", "rotationStartedAt", "previousExpiresAt", "generation", "phase"} {
		if rawField, exists := fields[name]; !exists || strings.TrimSpace(string(rawField)) == "null" {
			return value, false
		}
	}
	if json.Unmarshal([]byte(raw), &value) != nil ||
		value.SchemaVersion != 1 || !validOriginProof(value.Current) || !originGeneration.MatchString(value.Generation) {
		return originProofSet{}, false
	}
	if value.RotationStartedAt > 9007199254740991 || value.PreviousExpiresAt > 9007199254740991 {
		return originProofSet{}, false
	}
	if value.Phase == "seeded" || value.Phase == "active" {
		return value, value.Previous == "" && value.PreviousExpiresAt == 0 && value.RotationStartedAt >= 0
	}
	return value, value.Phase == "rotating" && validOriginProof(value.Previous) &&
		value.Previous != value.Current && value.RotationStartedAt > 0 &&
		value.PreviousExpiresAt > value.RotationStartedAt &&
		value.PreviousExpiresAt-value.RotationStartedAt <= originMaxOverlapSeconds
}

func originHeader(headers http.Header) (string, bool) {
	actual, found := "", false
	for name, values := range headers {
		if !strings.EqualFold(name, "x-origin-verify") {
			continue
		}
		if found || len(values) != 1 || !validOriginProof(values[0]) {
			return "", false
		}
		actual, found = values[0], true
	}
	return actual, found
}

func (v *OriginVerifier) cached() (originProofSet, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.proofs, v.proofs.Current != "" && v.now().Before(v.expires)
}

func (v *OriginVerifier) secret(ctx context.Context) (originProofSet, bool) {
	if v.client == nil {
		return originProofSet{}, false
	}
	if value, ok := v.cached(); ok {
		return value, true
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	select {
	case v.lookup <- struct{}{}:
		defer func() { <-v.lookup }()
	case <-bounded.Done():
		return originProofSet{}, false
	}
	if bounded.Err() != nil {
		return originProofSet{}, false
	}
	if value, ok := v.cached(); ok {
		return value, true
	}
	out, err := v.client.GetSecretValue(bounded, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(v.arn),
	}, func(options *secretsmanager.Options) { options.Retryer = aws.NopRetryer{} })
	if err != nil || out == nil || out.SecretString == nil {
		return originProofSet{}, false
	}
	value, ok := parseOriginProofSet(*out.SecretString)
	if !ok {
		return originProofSet{}, false
	}
	v.mu.Lock()
	v.proofs, v.expires = value, v.now().Add(originCacheTTL)
	v.mu.Unlock()
	return value, true
}

func (v *OriginVerifier) allows(ctx context.Context, headers http.Header) bool {
	if v == nil {
		return false
	}
	if v.arn == "" && v.legacy == "" && v.local {
		return true
	}
	actual, ok := originHeader(headers)
	if !ok {
		return false
	}
	if v.arn == "" {
		return validOriginProof(v.legacy) && subtle.ConstantTimeCompare([]byte(actual), []byte(v.legacy)) == 1
	}
	value, ok := v.secret(ctx)
	if !ok {
		return false
	}
	current := subtle.ConstantTimeCompare([]byte(actual), []byte(value.Current))
	previous := subtle.ConstantTimeCompare([]byte(actual), []byte(value.Previous))
	now := v.now().Unix()
	return current == 1 || (previous == 1 && value.Phase == "rotating" &&
		now >= value.RotationStartedAt && now < value.PreviousExpiresAt)
}

func (v *OriginVerifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !v.allows(r.Context(), r.Header) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"FORBIDDEN","message":"direct access not allowed"}}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
