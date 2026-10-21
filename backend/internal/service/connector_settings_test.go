package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type settingsRepoFixture struct {
	view       model.ConnectorSettings
	readUser   string
	approval   *repository.ConnectorApprovalUpdate
	binding    []string
	generation int64
	policyCaps []string
	err        error
}

func (r *settingsRepoFixture) ConnectorSettings(_ context.Context, user, client, audience, issuer string) (model.ConnectorSettings, error) {
	r.readUser = user
	return r.view, r.err
}
func (r *settingsRepoFixture) UpdateConnectorApprovalBound(_ context.Context, in repository.ConnectorApprovalUpdate, audience, issuer string, generation int64) (int64, error) {
	r.approval, r.binding, r.generation = &in, []string{audience, issuer}, generation
	return in.ExpectedGeneration + 1, r.err
}
func (r *settingsRepoFixture) SetConnectorPolicy(_ context.Context, actor, client, audience, issuer string, enabled bool, generation int64, caps []string) (int64, error) {
	r.policyCaps = caps
	return generation + 1, r.err
}
func settingsServiceFixture(r *settingsRepoFixture) *ConnectorSettingsService {
	s := NewConnectorSettingsService(r, ConnectorSettingsConfig{ReadClientID: "readclient", WriteClientID: "writeclient",
		Audience: "https://app.invalid/api/mcp", Issuer: "https://cognito-idp.ap-northeast-2.amazonaws.com/pool"})
	s.now = func() time.Time { return time.Unix(1800000000, 0) }
	return s
}
func TestConnectorSettingsApprovalOwnsIdentityCapabilitiesExpiryAndBinding(t *testing.T) {
	r := &settingsRepoFixture{view: model.ConnectorSettings{Enabled: true, PolicyGeneration: 3}}
	s := settingsServiceFixture(r)
	generation, err := s.UpdateApproval(context.Background(), "verified-user", ConnectorApprovalRequest{Profile: "write", Action: "approve_write"})
	if err != nil || generation != 1 || r.approval == nil {
		t.Fatalf("approval: %d %v", generation, err)
	}
	if r.readUser != "verified-user" || r.approval.UserID != "verified-user" || r.approval.ClientID != "writeclient" ||
		!reflect.DeepEqual(r.approval.Capabilities, []string{"read", "write"}) || r.approval.ExpiresAt != 1800003600 ||
		r.generation != 3 || !reflect.DeepEqual(r.binding, []string{s.config.Audience, s.config.Issuer}) {
		t.Fatalf("unbound approval: %+v %+v", r.approval, r.binding)
	}
	r.approval = nil
	_, err = s.UpdateApproval(context.Background(), "verified-user", ConnectorApprovalRequest{Profile: "read", Action: "approve_write"})
	if !errors.Is(err, ErrInvalidInput) || r.approval != nil {
		t.Fatal("read profile escalated")
	}
}
func TestConnectorSettingsRevocationPreservesBlockAndRejectsStaleGeneration(t *testing.T) {
	r := &settingsRepoFixture{view: model.ConnectorSettings{Enabled: false, PolicyGeneration: 8,
		Approval: &model.ConnectorApprovalState{Generation: 4, Blocked: true}}}
	s := settingsServiceFixture(r)
	_, err := s.UpdateApproval(context.Background(), "user", ConnectorApprovalRequest{Profile: "write", Action: "revoke", ExpectedGeneration: 4})
	if err != nil || !r.approval.Block || !r.approval.Revoke {
		t.Fatal("revocation removed block or required enabled policy")
	}
	r.approval = nil
	_, err = s.UpdateApproval(context.Background(), "user", ConnectorApprovalRequest{Profile: "write", Action: "allow_read", ExpectedGeneration: 3})
	if !errors.Is(err, repository.ErrConditionFailed) || r.approval != nil {
		t.Fatal("stale generation was written")
	}
}
func TestConnectorPolicyNeedsAdminAndServerOwnedProfile(t *testing.T) {
	r := &settingsRepoFixture{}
	s := settingsServiceFixture(r)
	for _, request := range []ConnectorPolicyRequest{{Profile: "write"}, {Profile: "arbitrary"}, {Profile: "read", ExpectedGeneration: -1}} {
		if _, err := s.UpdatePolicy(context.Background(), "user", false, request); !errors.Is(err, ErrForbidden) || r.policyCaps != nil {
			t.Fatal("non-admin changed policy")
		}
	}
	if _, err := s.UpdatePolicy(context.Background(), "admin", true, ConnectorPolicyRequest{Profile: "write"}); err != nil ||
		!reflect.DeepEqual(r.policyCaps, []string{"read", "write"}) {
		t.Fatal("policy profile is not server owned")
	}
}
func TestConnectorSettingsDefaultOffAndLookupFailureAreDistinct(t *testing.T) {
	r := &settingsRepoFixture{}
	empty := NewConnectorSettingsService(r, ConnectorSettingsConfig{})
	if rows, err := empty.List(context.Background(), "user"); err != nil || len(rows) != 0 || r.readUser != "" {
		t.Fatal("disabled rollout reads storage")
	}
	r.err = errors.New("lookup unavailable")
	if rows, err := settingsServiceFixture(r).List(context.Background(), "user"); err == nil || rows != nil {
		t.Fatal("lookup failure became empty configuration")
	}
}
