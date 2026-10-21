package service

import (
	"context"
	"regexp"
	"slices"
	"time"

	"github.com/ttobak/backend/internal/connector"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type connectorSettingsRepository interface {
	ConnectorSettings(context.Context, string, string, string, string) (model.ConnectorSettings, error)
	UpdateConnectorApprovalBound(context.Context, repository.ConnectorApprovalUpdate, string, string, int64) (int64, error)
	SetConnectorPolicy(context.Context, string, string, string, string, bool, int64, []string) (int64, error)
}

type ConnectorSettingsConfig struct{ ReadClientID, WriteClientID, Audience, Issuer string }
type ConnectorApprovalRequest struct {
	Profile            string `json:"profile"`
	Action             string `json:"action"`
	ExpectedGeneration int64  `json:"expectedGeneration"`
}
type ConnectorPolicyRequest struct {
	Profile            string `json:"profile"`
	Enabled            bool   `json:"enabled"`
	ExpectedGeneration int64  `json:"expectedGeneration"`
}
type ConnectorSettingsService struct {
	repo   connectorSettingsRepository
	config ConnectorSettingsConfig
	now    func() time.Time
}

var connectorSettingsUser = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var connectorSettingsClient = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)

func NewConnectorSettingsService(repo connectorSettingsRepository, config ConnectorSettingsConfig) *ConnectorSettingsService {
	return &ConnectorSettingsService{repo: repo, config: config, now: time.Now}
}

func (s *ConnectorSettingsService) client(profile string) (string, error) {
	client := s.config.ReadClientID
	switch profile {
	case "read":
	case "write":
		client = s.config.WriteClientID
	default:
		return "", ErrInvalidInput
	}
	if !connectorSettingsClient.MatchString(client) || s.config.Audience == "" || s.config.Issuer == "" ||
		(s.config.ReadClientID != "" && s.config.ReadClientID == s.config.WriteClientID) {
		return "", connector.ErrPolicyUnavailable
	}
	return client, nil
}

func (s *ConnectorSettingsService) List(ctx context.Context, user string) ([]model.ConnectorSettings, error) {
	if !connectorSettingsUser.MatchString(user) {
		return nil, ErrForbidden
	}
	result := []model.ConnectorSettings{}
	for _, profile := range []string{"read", "write"} {
		if (profile == "read" && s.config.ReadClientID == "") || (profile == "write" && s.config.WriteClientID == "") {
			continue
		}
		client, err := s.client(profile)
		if err != nil {
			return nil, err
		}
		view, err := s.repo.ConnectorSettings(ctx, user, client, s.config.Audience, s.config.Issuer)
		if err != nil {
			return nil, err
		}
		view.Profile = profile
		result = append(result, view)
	}
	return result, nil
}

func (s *ConnectorSettingsService) UpdateApproval(ctx context.Context, user string, request ConnectorApprovalRequest) (int64, error) {
	if !connectorSettingsUser.MatchString(user) {
		return 0, ErrForbidden
	}
	if request.ExpectedGeneration < 0 || request.ExpectedGeneration >= 9007199254740991 ||
		!slices.Contains([]string{"allow_read", "approve_write", "revoke", "block"}, request.Action) ||
		(request.Action == "approve_write" && request.Profile != "write") {
		return 0, ErrInvalidInput
	}
	client, err := s.client(request.Profile)
	if err != nil {
		return 0, err
	}
	view, err := s.repo.ConnectorSettings(ctx, user, client, s.config.Audience, s.config.Issuer)
	if err != nil {
		return 0, err
	}
	generation := int64(0)
	if view.Approval != nil {
		generation = view.Approval.Generation
	}
	if generation != request.ExpectedGeneration {
		return 0, repository.ErrConditionFailed
	}
	in := repository.ConnectorApprovalUpdate{
		UserID: user, ClientID: client, ExpectedGeneration: generation,
		Capabilities: []string{"read"}, ExpiresAt: s.now().Unix() + 3600,
		Revoke: request.Action == "revoke" || request.Action == "block", Block: request.Action == "block",
	}
	if request.Action == "approve_write" {
		in.Capabilities = []string{"read", "write"}
	}
	if request.Action == "revoke" && view.Approval != nil && view.Approval.Blocked {
		in.Block = true
	}
	return s.repo.UpdateConnectorApprovalBound(ctx, in, s.config.Audience, s.config.Issuer, view.PolicyGeneration)
}

func (s *ConnectorSettingsService) UpdatePolicy(ctx context.Context, actor string, admin bool, request ConnectorPolicyRequest) (int64, error) {
	if !admin || !connectorSettingsUser.MatchString(actor) {
		return 0, ErrForbidden
	}
	if request.ExpectedGeneration < 0 || request.ExpectedGeneration >= 9007199254740991 {
		return 0, ErrInvalidInput
	}
	client, err := s.client(request.Profile)
	if err != nil {
		return 0, err
	}
	maximum := []string{"read"}
	if request.Profile == "write" {
		maximum = []string{"read", "write"}
	}
	return s.repo.SetConnectorPolicy(ctx, actor, client, s.config.Audience, s.config.Issuer,
		request.Enabled, request.ExpectedGeneration, maximum)
}
