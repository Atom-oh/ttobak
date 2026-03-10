package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
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

// parseJWT parses a standard JWT and extracts claims
// JWT format: header.payload.signature (base64url encoded)
// Works with both ALB OIDC JWTs and Cognito JWTs
func parseJWT(token string) (*ALBOIDCClaims, error) {
	// Split JWT into parts
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}

	// Decode payload (second part)
	payload, err := base64URLDecode(parts[1])
	if err != nil {
		return nil, err
	}

	var claims ALBOIDCClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, err
	}

	return &claims, nil
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

// CORS middleware adds CORS headers to responses
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-amzn-oidc-data")
		w.Header().Set("Access-Control-Max-Age", "86400")

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
