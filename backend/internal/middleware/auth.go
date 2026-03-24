package middleware

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ContextKey is a custom type for context keys
type ContextKey string

const (
	// UserIDKey is the context key for user ID
	UserIDKey ContextKey = "userId"
	// UserEmailKey is the context key for user email
	UserEmailKey ContextKey = "userEmail"
	// UserNameKey is the context key for user name
	UserNameKey ContextKey = "userName"
)

// JWKS types for Cognito public key fetching
type jwksResponse struct {
	Keys []jwkKey `json:"keys"`
}

type jwkKey struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Use string `json:"use"`
}

// jwksCache holds cached JWKS keys with TTL
var jwksCache struct {
	sync.RWMutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

var (
	cognitoRegion     = getEnvOrDefault("COGNITO_REGION", "ap-northeast-2")
	cognitoUserPoolID = os.Getenv("COGNITO_USER_POOL_ID")
)

func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// ALBOIDCClaims represents the claims in the ALB OIDC JWT
type ALBOIDCClaims struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	GivenName     string `json:"given_name"`
	FamilyName    string `json:"family_name"`
	Exp           int64  `json:"exp"`
	Iss           string `json:"iss"`
	TokenUse      string `json:"token_use"`
}

// Auth is middleware that extracts user information from JWT
// Supports both:
// 1. Authorization: Bearer <jwt> header (Cognito JWT via API Gateway/Lambda@Edge)
// 2. x-amzn-oidc-data header (ALB OIDC) for backward compatibility
func Auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var claims *ALBOIDCClaims
		var err error

		// First, check for Authorization: Bearer <token> header
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
			token := strings.TrimPrefix(authHeader, "Bearer ")
			claims, err = parseJWT(token)
			if err != nil {
				http.Error(w, `{"error":"unauthorized","message":"invalid authorization token"}`, http.StatusUnauthorized)
				return
			}
		} else {
			// Fall back to ALB OIDC header
			oidcData := r.Header.Get("x-amzn-oidc-data")
			if oidcData == "" {
				http.Error(w, `{"error":"unauthorized","message":"missing authentication"}`, http.StatusUnauthorized)
				return
			}

			claims, err = parseALBJWT(oidcData)
			if err != nil {
				http.Error(w, `{"error":"unauthorized","message":"invalid authentication token"}`, http.StatusUnauthorized)
				return
			}
		}

		if claims.Sub == "" {
			http.Error(w, `{"error":"unauthorized","message":"invalid user identity"}`, http.StatusUnauthorized)
			return
		}

		// Add user info to context
		ctx := r.Context()
		ctx = context.WithValue(ctx, UserIDKey, claims.Sub)
		ctx = context.WithValue(ctx, UserEmailKey, claims.Email)

		// Build user name from available fields
		name := claims.Name
		if name == "" && (claims.GivenName != "" || claims.FamilyName != "") {
			name = strings.TrimSpace(claims.GivenName + " " + claims.FamilyName)
		}
		ctx = context.WithValue(ctx, UserNameKey, name)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// parseALBJWT parses the ALB OIDC JWT and extracts claims
// ALB JWT format: header.payload.signature (base64url encoded)
func parseALBJWT(token string) (*ALBOIDCClaims, error) {
	return parseJWT(token)
}

// parseJWT parses and verifies a JWT token.
// If COGNITO_USER_POOL_ID is set, performs full signature verification.
// Otherwise falls back to unverified decode (backward compatibility).
func parseJWT(token string) (*ALBOIDCClaims, error) {
	if cognitoUserPoolID != "" {
		return parseVerifiedJWT(token)
	}
	return parseUnverifiedJWT(token)
}

// parseVerifiedJWT verifies JWT signature using Cognito JWKS
func parseVerifiedJWT(tokenStr string) (*ALBOIDCClaims, error) {
	expectedIssuer := fmt.Sprintf("https://cognito-idp.%s.amazonaws.com/%s", cognitoRegion, cognitoUserPoolID)

	parsed, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
		// Verify signing method
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		kid, ok := token.Header["kid"].(string)
		if !ok {
			return nil, fmt.Errorf("kid not found in token header")
		}

		keys, err := getJWKSKeys()
		if err != nil {
			return nil, err
		}

		key, ok := keys[kid]
		if !ok {
			return nil, fmt.Errorf("key %s not found in JWKS", kid)
		}

		return key, nil
	}, jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(expectedIssuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("token verification failed: %w", err)
	}

	mapClaims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("failed to extract claims")
	}

	claims := &ALBOIDCClaims{
		Sub:        getStringClaim(mapClaims, "sub"),
		Email:      getStringClaim(mapClaims, "email"),
		Name:       getStringClaim(mapClaims, "name"),
		GivenName:  getStringClaim(mapClaims, "given_name"),
		FamilyName: getStringClaim(mapClaims, "family_name"),
		Iss:        getStringClaim(mapClaims, "iss"),
	}
	if v, ok := mapClaims["email_verified"].(bool); ok {
		claims.EmailVerified = v
	}
	if v, ok := mapClaims["exp"].(float64); ok {
		claims.Exp = int64(v)
	}

	return claims, nil
}

func getStringClaim(claims jwt.MapClaims, key string) string {
	if v, ok := claims[key].(string); ok {
		return v
	}
	return ""
}

// parseUnverifiedJWT decodes JWT payload with lightweight validation (fallback)
// Provides defense-in-depth when JWKS verification is not available:
// - Validates token has 3 parts (header.payload.signature)
// - Validates issuer matches expected Cognito URL (if pool ID known)
// - Validates token is not expired
// - Validates token_use is "id" (not "access")
func parseUnverifiedJWT(token string) (*ALBOIDCClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}

	payload, err := base64URLDecode(parts[1])
	if err != nil {
		return nil, err
	}

	var claims ALBOIDCClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}

	// Defense-in-depth: lightweight validation even without signature verification
	if err := validateClaimsLightweight(&claims); err != nil {
		return nil, err
	}

	return &claims, nil
}

// validateClaimsLightweight performs lightweight JWT claim validation
// for defense-in-depth when full signature verification is not available
func validateClaimsLightweight(claims *ALBOIDCClaims) error {
	// Validate token is not expired
	if claims.Exp > 0 && time.Now().Unix() > claims.Exp {
		return &AuthError{Message: "token expired"}
	}

	// Validate issuer if we know the expected pool
	if cognitoUserPoolID != "" && claims.Iss != "" {
		// Extract region from pool ID (format: {region}_{poolId})
		region := cognitoRegion
		if idx := strings.Index(cognitoUserPoolID, "_"); idx > 0 {
			region = cognitoUserPoolID[:idx]
		}
		expectedIssuer := fmt.Sprintf("https://cognito-idp.%s.amazonaws.com/%s", region, cognitoUserPoolID)
		if claims.Iss != expectedIssuer {
			return &AuthError{Message: "invalid token issuer"}
		}
	}

	// Validate token_use is "id" (not "access") for ID tokens
	// Allow empty token_use for backward compatibility with ALB OIDC tokens
	if claims.TokenUse != "" && claims.TokenUse != "id" {
		return &AuthError{Message: "invalid token_use: expected id token"}
	}

	return nil
}

// getJWKSKeys fetches and caches JWKS keys from Cognito
func getJWKSKeys() (map[string]*rsa.PublicKey, error) {
	jwksCache.RLock()
	if jwksCache.keys != nil && time.Since(jwksCache.fetchedAt) < time.Hour {
		keys := jwksCache.keys
		jwksCache.RUnlock()
		return keys, nil
	}
	jwksCache.RUnlock()

	jwksCache.Lock()
	defer jwksCache.Unlock()

	// Double-check after acquiring write lock
	if jwksCache.keys != nil && time.Since(jwksCache.fetchedAt) < time.Hour {
		return jwksCache.keys, nil
	}

	jwksURL := fmt.Sprintf("https://cognito-idp.%s.amazonaws.com/%s/.well-known/jwks.json", cognitoRegion, cognitoUserPoolID)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read JWKS response: %w", err)
	}

	var jwks jwksResponse
	if err := json.Unmarshal(body, &jwks); err != nil {
		return nil, fmt.Errorf("failed to parse JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey)
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" || k.Use != "sig" {
			continue
		}
		pubKey, err := parseRSAPublicKey(k.N, k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = pubKey
	}

	jwksCache.keys = keys
	jwksCache.fetchedAt = time.Now()
	return keys, nil
}

func parseRSAPublicKey(nStr, eStr string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil {
		return nil, err
	}

	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)

	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}

// base64URLDecode decodes a base64url encoded string
func base64URLDecode(s string) ([]byte, error) {
	// Add padding if necessary
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}

	return base64.URLEncoding.DecodeString(s)
}

// ErrInvalidToken is returned when the JWT token is invalid
var ErrInvalidToken = &AuthError{Message: "invalid token format"}

// AuthError represents an authentication error
type AuthError struct {
	Message string
}

func (e *AuthError) Error() string {
	return e.Message
}

// GetUserID extracts the user ID from the request context
func GetUserID(ctx context.Context) string {
	if userID, ok := ctx.Value(UserIDKey).(string); ok {
		return userID
	}
	return ""
}

// GetUserEmail extracts the user email from the request context
func GetUserEmail(ctx context.Context) string {
	if email, ok := ctx.Value(UserEmailKey).(string); ok {
		return email
	}
	return ""
}

// GetUserName extracts the user name from the request context
func GetUserName(ctx context.Context) string {
	if name, ok := ctx.Value(UserNameKey).(string); ok {
		return name
	}
	return ""
}

// allowedOrigins for CORS validation
var allowedOrigins = map[string]bool{
	"https://d115v97ubjhb06.cloudfront.net": true,
	"http://localhost:3000":                 true,
}

// CORS middleware adds CORS headers to responses
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Only set CORS headers for allowed origins
		if allowedOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-amzn-oidc-data")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.Header().Set("Vary", "Origin")
		}

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// JSON middleware sets content type to application/json
func JSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}

// Recovery middleware recovers from panics
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"internal_error","message":"an unexpected error occurred"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
