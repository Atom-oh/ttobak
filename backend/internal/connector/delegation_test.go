package connector

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

type fixtures struct {
	KeyID     string `json:"keyId"`
	SPKI      string `json:"publicKeySPKI"`
	Now       int64  `json:"now"`
	Arguments string `json:"arguments"`
	Cases     []struct {
		Name, Wire, Operation, Expected string
		Capabilities                    []string
	} `json:"cases"`
}

func fixtureData(t *testing.T) fixtures {
	t.Helper()
	raw, err := os.ReadFile("../../../contracts/connector/v1/delegation-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixtures
	if err = json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}
func fixtureGrants(user, client string) Grants {
	caps := []string{"delete", "read", "share", "write"}
	return Grants{user, client, true, caps, &Approval{user, client, 7, caps, 1800004000, false}}
}
func fixtureVerifier(t *testing.T, f fixtures, resolve GrantResolver, clock func() int64) *Verifier {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(f.SPKI)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier(f.KeyID, key, "ttobak-api-bridge", resolve, clock)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestSharedKMSVectors(t *testing.T) {
	f := fixtureData(t)
	v := fixtureVerifier(t, f, func(_ context.Context, u, c string) (Grants, error) { return fixtureGrants(u, c), nil }, func() int64 { return f.Now })
	for _, test := range f.Cases {
		t.Run(test.Name, func(t *testing.T) {
			p, err := v.Verify(context.Background(), []byte(test.Wire), Request{test.Operation, "fixture-request", []byte(f.Arguments)})
			if test.Expected == "OK" {
				if err != nil || !slices.Equal(p.Capabilities(), test.Capabilities) {
					t.Fatalf("valid vector rejected: %v", err)
				}
				if err = p.Require(test.Operation, "ttobak-api-bridge", f.Now); err != nil {
					t.Fatal(err)
				}
				caps := p.Capabilities()
				caps[0] = "invalid"
				if p.Capabilities()[0] == "invalid" {
					t.Fatal("mutable principal")
				}
			} else if err == nil || err.Error() != test.Expected {
				t.Fatalf("got %v, want %s", err, test.Expected)
			}
		})
	}
	var e envelope
	_ = json.Unmarshal([]byte(f.Cases[1].Wire), &e)
	raw, _ := base64.RawURLEncoding.DecodeString(e.Payload)
	digest, err := SigningDigest(raw)
	expected := sha256.Sum256(raw)
	if err != nil || !slices.Equal(digest, expected[:]) || MessageType != "DIGEST" {
		t.Fatal("KMS digest convention changed")
	}
}
func TestDelegationRequestAndPolicyGuards(t *testing.T) {
	f := fixtureData(t)
	test := f.Cases[1]
	clock := f.Now
	v := fixtureVerifier(t, f, func(_ context.Context, u, c string) (Grants, error) { return fixtureGrants(u, c), nil }, func() int64 { return clock })
	request := Request{test.Operation, "fixture-request", []byte(f.Arguments)}
	for name, edit := range map[string]func(*Request){
		"body changed":       func(r *Request) { r.Arguments = []byte("{}") },
		"operation changed":  func(r *Request) { r.Operation = "api.meeting.deleteMeeting" },
		"request id changed": func(r *Request) { r.RequestID = "different" },
		"invalid utf8":       func(r *Request) { r.Arguments = []byte{0xff} },
		"oversized":          func(r *Request) { r.Arguments = make([]byte, MaxArgumentBytes+1) },
	} {
		t.Run(name, func(t *testing.T) {
			r := request
			edit(&r)
			if _, err := v.Verify(context.Background(), []byte(test.Wire), r); err == nil {
				t.Fatal("forged request accepted")
			}
		})
	}
	wire := []byte(test.Wire)
	if _, err := v.Verify(context.Background(), append([]byte(" "), wire...), request); !errors.Is(err, ErrInvalidDelegation) {
		t.Fatal("noncanonical wire accepted")
	}
	duplicate := strings.Replace(test.Wire, `"keyId":`, `"keyId":"duplicate","keyId":`, 1)
	if _, err := v.Verify(context.Background(), []byte(duplicate), request); !errors.Is(err, ErrInvalidDelegation) {
		t.Fatal("duplicate wrapper accepted")
	}
	var e envelope
	_ = json.Unmarshal(wire, &e)
	e.Signature = strings.Repeat("A", len(e.Signature))
	forged, _ := json.Marshal(e)
	if _, err := v.Verify(context.Background(), forged, request); !errors.Is(err, ErrInvalidSignature) {
		t.Fatal("forged signature accepted")
	}
	v = fixtureVerifier(t, f, func(_ context.Context, u, c string) (Grants, error) {
		clock = 1800000050
		return fixtureGrants(u, c), nil
	}, func() int64 { return clock })
	if _, err := v.Verify(context.Background(), wire, request); !errors.Is(err, ErrExpired) {
		t.Fatal("expired during lookup accepted")
	}
	clock = f.Now
	v = fixtureVerifier(t, f, func(context.Context, string, string) (Grants, error) {
		return Grants{}, errors.New("private token fixture")
	}, func() int64 { return clock })
	if _, err := v.Verify(context.Background(), wire, request); !errors.Is(err, ErrPolicyUnavailable) || strings.Contains(err.Error(), "private") {
		t.Fatal("resolver diagnostic leaked")
	}
}
