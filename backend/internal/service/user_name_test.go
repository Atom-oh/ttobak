package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cognitoidp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cognitoidptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
)

func TestNormalizeUserName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "trims surrounding space", in: "  홍길동 ", want: "홍길동"},
		{name: "keeps inner space", in: "Gil Dong Hong", want: "Gil Dong Hong"},
		{name: "empty", in: "", wantErr: true},
		{name: "blank", in: " \t ", wantErr: true},
		{name: "limit in code points", in: strings.Repeat("가", maxUserNameRunes), want: strings.Repeat("가", maxUserNameRunes)},
		{name: "over limit", in: strings.Repeat("가", maxUserNameRunes+1), wantErr: true},
		{name: "newline", in: "홍\n길동", wantErr: true},
		{name: "nul", in: "홍\x00길동", wantErr: true},
		{name: "bidi override", in: "홍‮길동", wantErr: true},
		{name: "invalid utf8", in: "홍\xff", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeUserName(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidUserName) {
					t.Fatalf("NormalizeUserName(%q) error = %v, want ErrInvalidUserName", tc.in, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeUserName(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestUpdateUserNameWritesCognitoThenProfile(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{}
	repo := &fakeUserAdminRepo{}
	svc := NewUserAdminServiceForTest(cognito, "pool-1", repo)

	resp, err := svc.UpdateUserName(context.Background(), "user-1", "  홍길동 ")
	if err != nil {
		t.Fatalf("UpdateUserName: %v", err)
	}
	if resp.UserID != "user-1" || resp.Name != "홍길동" {
		t.Fatalf("response = %+v", resp)
	}
	if len(cognito.updateAttrCalls) != 1 {
		t.Fatalf("AdminUpdateUserAttributes calls = %d, want 1", len(cognito.updateAttrCalls))
	}
	call := cognito.updateAttrCalls[0]
	if aws.ToString(call.UserPoolId) != "pool-1" || aws.ToString(call.Username) != "user-1" {
		t.Fatalf("call target = %s/%s", aws.ToString(call.UserPoolId), aws.ToString(call.Username))
	}
	if len(call.UserAttributes) != 1 || aws.ToString(call.UserAttributes[0].Name) != "name" || aws.ToString(call.UserAttributes[0].Value) != "홍길동" {
		t.Fatalf("only the name attribute may be written, got %+v", call.UserAttributes)
	}
	if repo.nameUpdates["user-1"] != "홍길동" {
		t.Fatalf("profile name = %q", repo.nameUpdates["user-1"])
	}
}

func TestUpdateUserNameRejectsInvalidBeforeAnyWrite(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{}
	repo := &fakeUserAdminRepo{}
	svc := NewUserAdminServiceForTest(cognito, "pool-1", repo)

	if _, err := svc.UpdateUserName(context.Background(), "user-1", "   "); !errors.Is(err, ErrInvalidUserName) {
		t.Fatalf("err = %v, want ErrInvalidUserName", err)
	}
	if _, err := svc.UpdateUserName(context.Background(), "", "홍길동"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("empty target err = %v, want ErrUserNotFound", err)
	}
	if len(cognito.updateAttrCalls) != 0 || len(repo.nameUpdates) != 0 {
		t.Fatal("invalid input must not reach Cognito or DynamoDB")
	}
}

func TestUpdateUserNameMapsMissingUser(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{adminUpdateAttributesFn: func(context.Context, *cognitoidp.AdminUpdateUserAttributesInput) (*cognitoidp.AdminUpdateUserAttributesOutput, error) {
		return nil, &cognitoidptypes.UserNotFoundException{}
	}}
	repo := &fakeUserAdminRepo{}
	svc := NewUserAdminServiceForTest(cognito, "pool-1", repo)

	if _, err := svc.UpdateUserName(context.Background(), "ghost", "홍길동"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
	if len(repo.nameUpdates) != 0 {
		t.Fatal("profile must not change when Cognito rejects the user")
	}
}

func TestUpdateUserNameSurfacesProfileSyncFailure(t *testing.T) {
	cognito := &fakeCognitoAdminAPI{}
	repo := &fakeUserAdminRepo{nameErr: errors.New("throttled")}
	svc := NewUserAdminServiceForTest(cognito, "pool-1", repo)

	_, err := svc.UpdateUserName(context.Background(), "user-1", "홍길동")
	if !errors.Is(err, ErrUserNameProfileSync) {
		t.Fatalf("err = %v, want ErrUserNameProfileSync", err)
	}
}
