package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	idp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	ct "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
)

type creationCognito struct{ *stubCognitoAdminAPI }

func (creationCognito) AdminGetUser(context.Context, *idp.AdminGetUserInput, ...func(*idp.Options)) (*idp.AdminGetUserOutput, error) {
	return &idp.AdminGetUserOutput{Enabled: true, UserStatus: ct.UserStatusTypeConfirmed, UserAttributes: []ct.AttributeType{
		{Name: aws.String("sub"), Value: aws.String("colleague-sub")},
		{Name: aws.String("email"), Value: aws.String("colleague@example.com")},
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
