package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	idp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	ct "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type creationCognito struct{ *stubCognitoAdminAPI }

func (creationCognito) AdminGetUser(context.Context, *idp.AdminGetUserInput, ...func(*idp.Options)) (*idp.AdminGetUserOutput, error) {
	return &idp.AdminGetUserOutput{Enabled: true, UserStatus: ct.UserStatusTypeConfirmed, UserAttributes: []ct.AttributeType{
		{Name: aws.String("sub"), Value: aws.String("colleague-sub")},
		{Name: aws.String("email"), Value: aws.String("colleague@example.com")},
		{Name: aws.String("email_verified"), Value: aws.String("true")},
	}}, nil
}

func TestAccountCreation_ActualOwnerAndCheckboxReceipt(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "invite-only", true: "creator-AM"}[keep], func(t *testing.T) {
			h, repo := newStubAccountHandler()
			h.accountService.SetCognitoAdminAPI(creationCognito{&stubCognitoAdminAPI{}}, "pool")
			payload, _ := json.Marshal(model.CreateAccountRequest{Name: "Prepared", OwnerEmail: "colleague@example.com", KeepCreatorAsMember: keep})
			req := withUserEmailCtx(httptest.NewRequest(http.MethodPost, "/api/accounts", strings.NewReader(string(payload))), "creator", "creator@example.com")
			out := httptest.NewRecorder()
			h.CreateAccount(out, req)
			var receipt model.AccountCreationResponse
			if out.Code != 201 {
				t.Fatalf("creation status=%d %s", out.Code, out.Body)
			}
			if err := json.Unmarshal(out.Body.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.OwnerUserID != "colleague-sub" || receipt.CreatorMember != keep {
				t.Fatalf("false receipt: %+v", receipt)
			}
			creator := repo.members[acctMemberKey(receipt.AccountID, "creator")]
			if keep && (creator == nil || creator.Role != model.RoleAM) || !keep && creator != nil {
				t.Fatalf("creator grant: %+v", creator)
			}
			detailReq := withChiParam(withUserEmailCtx(httptest.NewRequest(http.MethodGet, "/api/accounts/"+receipt.AccountID, nil), "creator", "creator@example.com"), "accountId", receipt.AccountID)
			detailOut := httptest.NewRecorder()
			h.GetAccount(detailOut, detailReq)
			want := http.StatusForbidden
			if keep {
				want = http.StatusOK
			}
			if detailOut.Code != want {
				t.Fatalf("creator read=%d want=%d", detailOut.Code, want)
			}
		})
	}
}

func TestOwnerInvitation_StrictBodyAndVerifiedCaller(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `"invalid"`, `{"email":"chosen@example.com"}`, `{} {}`, `{}` + strings.Repeat(" ", 2048)} {
		h, _ := newStubAccountHandler()
		req := withChiParam(withUserEmailCtx(httptest.NewRequest(http.MethodPost, "/api/accounts/account/owner-invitation", strings.NewReader(body)), "creator", "creator@example.com"), "accountId", "account")
		out := httptest.NewRecorder()
		h.ResendOwnerInvitation(out, req)
		if out.Code != http.StatusBadRequest {
			t.Fatalf("invalid owner resend=%d", out.Code)
		}
	}
	h, _ := newStubAccountHandler()
	req := withUserEmailCtx(httptest.NewRequest(http.MethodPost, "/api/accounts/account/owner-invitation", strings.NewReader(`{}`)), "creator", "creator@example.com")
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserEmailVerifiedKey, false))
	out := httptest.NewRecorder()
	h.ResendOwnerInvitation(out, req)
	if out.Code != http.StatusForbidden {
		t.Fatalf("unverified owner resend=%d", out.Code)
	}
}

func TestAccountCreation_StrictBoundedBody(t *testing.T) {
	for _, body := range []string{
		`{"name":"Customer"} {}`,
		`{"name":"Customer"}` + strings.Repeat(" ", 17*1024),
		`{"name":"Customer","ownerUserId":"forged-sub"}`,
	} {
		h, repo := newStubAccountHandler()
		req := withUserEmailCtx(httptest.NewRequest(http.MethodPost, "/api/accounts", strings.NewReader(body)), "creator", "creator@example.com")
		out := httptest.NewRecorder()
		h.CreateAccount(out, req)
		if out.Code != http.StatusBadRequest || len(repo.accounts) != 0 {
			t.Fatalf("invalid creation accepted: %d accounts=%d", out.Code, len(repo.accounts))
		}
	}
}

func TestAccountCreation_DesignatedOwnerRequiresVerifiedEmail(t *testing.T) {
	h, repo := newStubAccountHandler()
	req := withUserEmailCtx(httptest.NewRequest(http.MethodPost, "/api/accounts", strings.NewReader(`{"name":"Customer","ownerEmail":"new@example.com"}`)), "creator", "creator@example.com")
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserEmailVerifiedKey, false))
	out := httptest.NewRecorder()
	h.CreateAccount(out, req)
	if out.Code != http.StatusForbidden || len(repo.accounts) != 0 {
		t.Fatalf("unverified creator accepted: %d", out.Code)
	}
}

type transferHandlerRepo struct {
	*mockHandlerAccountRepo
	err error
}

func (r transferHandlerRepo) TransferAccountOwner(context.Context, string, string, string, time.Time) error {
	return r.err
}

func TestTransferOwnerHandler(t *testing.T) {
	post := func(h *AccountHandler, user, body string, verified bool) *httptest.ResponseRecorder {
		req := withChiParam(withUserEmailCtx(httptest.NewRequest(http.MethodPost, "/api/accounts/acc/owner-transfer", strings.NewReader(body)), user, "o@example.com"), "accountId", "acc")
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserEmailVerifiedKey, verified))
		out := httptest.NewRecorder()
		h.TransferOwner(out, req)
		return out
	}
	build := func(repoErr error, wired bool) *AccountHandler {
		base := newMockHandlerAccountRepo()
		base.accounts["acc"] = &model.Account{AccountID: "acc", OwnerUserID: "owner"}
		base.members[acctMemberKey("acc", "owner")] = &model.AccountMember{AccountID: "acc", UserID: "owner", Role: model.RoleOwner}
		base.members[acctMemberKey("acc", "colleague-sub")] = &model.AccountMember{AccountID: "acc", UserID: "colleague-sub", Role: model.RoleSA}
		base.members[acctMemberKey("acc", "member")] = &model.AccountMember{AccountID: "acc", UserID: "member", Role: model.RoleSA}
		svc := service.NewAccountServiceForTest(transferHandlerRepo{base, repoErr})
		if wired {
			svc.SetCognitoAdminAPI(creationCognito{&stubCognitoAdminAPI{}}, "pool")
		}
		return &AccountHandler{accountService: svc}
	}
	good := `{"targetUserId":"colleague-sub"}`
	for _, tc := range []struct {
		name     string
		h        *AccountHandler
		body     string
		verified bool
		want     int
		code     string
	}{
		{"unverified email", build(nil, true), good, false, http.StatusForbidden, model.ErrCodeForbidden},
		{"unknown field", build(nil, true), `{"targetUserId":"colleague-sub","role":"owner"}`, true, http.StatusBadRequest, model.ErrCodeBadRequest},
		{"trailing value", build(nil, true), good + ` {}`, true, http.StatusBadRequest, model.ErrCodeBadRequest},
		{"oversized", build(nil, true), good + strings.Repeat(" ", 2048), true, http.StatusBadRequest, model.ErrCodeBadRequest},
		{"self transfer", build(nil, true), `{"targetUserId":"owner"}`, true, http.StatusBadRequest, model.ErrCodeBadRequest},
		{"non-owner requester", build(nil, true), good, true, http.StatusForbidden, model.ErrCodeForbidden},
		{"target not a member", build(nil, true), `{"targetUserId":"nobody"}`, true, http.StatusNotFound, model.ErrCodeNotFound},
		{"conflict", build(fmt.Errorf("tx: %w", repository.ErrConditionFailed), true), good, true, http.StatusConflict, model.ErrCodeConflict},
		{"cognito unwired", build(nil, false), good, true, http.StatusServiceUnavailable, model.ErrCodeInternalError},
		{"success", build(nil, true), good, true, http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := "owner"
			if tc.name == "non-owner requester" {
				user = "member"
			}
			out := post(tc.h, user, tc.body, tc.verified)
			if out.Code != tc.want {
				t.Fatalf("status=%d want %d: %s", out.Code, tc.want, out.Body)
			}
			if tc.code != "" {
				var e struct {
					Error struct{ Code string } `json:"error"`
				}
				if err := json.Unmarshal(out.Body.Bytes(), &e); err != nil || e.Error.Code != tc.code {
					t.Fatalf("error body %s", out.Body)
				}
				return
			}
			var got model.TransferOwnerResponse
			if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || got.OwnerUserID != "colleague-sub" || got.PreviousOwnerRole != model.RoleSA {
				t.Fatalf("response %s", out.Body)
			}
		})
	}
}
