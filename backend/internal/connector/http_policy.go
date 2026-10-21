package connector

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
)

const ReadScope = "ttobak.connector/read"

var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)
var resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ReadOnlyHTTPPolicy is a server-owned ceiling for a dedicated read client.
// Call it only after cryptographic token verification. Resource authorization
// remains in the handler/service; this policy does not grant resource access.
type ReadOnlyHTTPPolicy struct {
	clientID   string
	audience   string
	webClients []string
}

func NewReadOnlyHTTPPolicy(clientID, audience string, webClients ...string) (*ReadOnlyHTTPPolicy, error) {
	if clientID == "" && audience == "" {
		return nil, nil // Reader-first rollout; no connector client exists yet.
	}
	u, err := url.Parse(audience)
	if !clientIDPattern.MatchString(clientID) || err != nil || u.Scheme != "https" ||
		u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.Path != "/api/mcp" || u.RawPath != "" || u.String() != audience {
		return nil, ErrInvalidConfig
	}
	if len(webClients) == 0 {
		return nil, ErrInvalidConfig
	}
	for _, id := range webClients {
		if !clientIDPattern.MatchString(id) || id == clientID {
			return nil, ErrInvalidConfig
		}
	}
	return &ReadOnlyHTTPPolicy{clientID: clientID, audience: audience, webClients: slices.Clone(webClients)}, nil
}

// Authorize returns whether this is the configured connector and its decision.
// ID tokens identify their client through aud: they must not fall through into
// ordinary web authorization merely because client_id is absent.
func (p *ReadOnlyHTTPPolicy) Authorize(tokenUse, clientID string, audiences []string, scopes, method, path string) (bool, error) {
	if p == nil {
		return false, nil
	}
	if clientID != p.clientID && !slices.Contains(audiences, p.clientID) {
		for _, scope := range strings.Fields(scopes) {
			if strings.HasPrefix(scope, "ttobak.connector/") {
				return true, ErrCapabilityDenied
			}
		}
		// The existing first-party allowlist remains authoritative. A policy
		// decision of false never grants ordinary application access.
		return false, nil
	}
	if tokenUse != "access" || clientID != p.clientID || len(audiences) != 1 || audiences[0] != p.audience {
		return true, ErrCapabilityDenied
	}
	granted := strings.Fields(scopes)
	if !slices.Contains(granted, ReadScope) {
		return true, ErrCapabilityDenied
	}
	for _, scope := range granted {
		if strings.HasPrefix(scope, "ttobak.connector/") && scope != ReadScope {
			return true, ErrCapabilityDenied
		}
	}
	operation, err := HTTPOperation(method, path)
	if err != nil {
		return true, err
	}
	required, err := RequiredCapabilities(operation, "ttobak-api-bridge")
	if err != nil || !slices.Equal(required, []string{"read"}) {
		return true, ErrCapabilityDenied
	}
	// Availability is narrower than the delegation inventory. These readers
	// already have bounded pages and no implicit profile/invitation writes.
	if !AvailableReadOperation(operation) {
		return true, ErrCapabilityDenied
	}
	return true, nil
}

// HTTPOperation matches the reviewed inventory. Literal segments take precedence
// over identifier slots, just as the chi router does. Encoded, empty and traversal
// segments reject rather than introducing a second path interpretation.
func HTTPOperation(method, path string) (string, error) {
	if strings.ContainsAny(path, `%\?#`) || !strings.HasPrefix(path, "/api/") {
		return "", ErrCapabilityDenied
	}
	if operation, ok := httpOperationBindings[method+" "+path]; ok {
		return operation, nil
	}
	parts := strings.Split(path, "/")
	best, bestLiterals := "", -1
	for binding, operation := range httpOperationBindings {
		verb, pattern, ok := strings.Cut(binding, " ")
		if !ok || verb != method {
			continue
		}
		template := strings.Split(pattern, "/")
		if len(parts) != len(template) {
			continue
		}
		match, literals := true, 0
		for i, segment := range template {
			if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
				if !resourceIDPattern.MatchString(parts[i]) {
					match = false
					break
				}
			} else if segment != parts[i] {
				match = false
				break
			} else {
				literals++
			}
		}
		if match && literals > bestLiterals {
			best, bestLiterals = operation, literals
		} else if match && literals == bestLiterals && operation != best {
			return "", ErrCapabilityDenied
		}
	}
	if best == "" {
		return "", ErrCapabilityDenied
	}
	return best, nil
}

// AuthorizeReadGrant checks a fresh server-side client row for generation-zero
// reads. It does not grant access to any domain resource.
func AuthorizeReadGrant(grants Grants, userID, clientID string) error {
	if !grants.ClientEnabled || grants.UserID != userID || grants.ClientID != clientID ||
		!validCapabilities(grants.ClientMaximum) {
		return ErrInvalidGrant
	}
	return nil
}
