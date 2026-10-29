package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

type descendantAccessRepo struct {
	accounts map[string]*model.Account
	members  map[string]*model.AccountMember
	meeting  *model.Meeting
	share    *model.Share
	err      error
}

func newDescendantAccessRepo() *descendantAccessRepo {
	r := &descendantAccessRepo{accounts: map[string]*model.Account{}, members: map[string]*model.AccountMember{}}
	r.account("child", "parent")
	r.account("parent", "root")
	r.account("root", "")
	r.member("parent", "reader", model.RoleOwner)
	r.meeting = &model.Meeting{PK: "USER#owner", SK: "MEETING#meeting", UserID: "owner", MeetingID: "meeting", AccountID: "child", SharedToAccount: true, Content: "approved",
		SummaryPrivacy: &model.SummaryPrivacy{Version: 1, State: model.SummaryPrivacySafe, GenerationID: "generation", ContentSHA256: model.SummaryPrivacyHash("approved")}}
	return r
}

func (r *descendantAccessRepo) account(id, parent string) {
	r.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META", AccountID: id, ParentAccountID: parent, EntityType: model.EntityTypeAccount}
}

func (r *descendantAccessRepo) member(id, user, role string) {
	r.members[id+"/"+user] = &model.AccountMember{PK: "ACCOUNT#" + id, SK: "MEMBER#" + user, AccountID: id, UserID: user, Role: role, EntityType: model.EntityTypeAccountMember, AddedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (r *descendantAccessRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	if r.err != nil {
		return nil, r.err
	}
	if a := r.accounts[id]; a != nil {
		copy := *a
		return &copy, nil
	}
	return nil, nil
}

func (r *descendantAccessRepo) GetMember(_ context.Context, id, user string) (*model.AccountMember, error) {
	if r.err != nil {
		return nil, r.err
	}
	if member := r.members[id+"/"+user]; member != nil {
		copy := *member
		return &copy, nil
	}
	return nil, nil
}

func (r *descendantAccessRepo) GetMeetingByID(context.Context, string) (*model.Meeting, error) {
	return r.meeting, nil
}

func (r *descendantAccessRepo) GetShare(context.Context, string, string) (*model.Share, error) {
	return r.share, nil
}

func TestDescendantMeetingReadAccess(t *testing.T) {
	for _, ancestor := range []string{"parent", "root"} {
		t.Run(ancestor, func(t *testing.T) {
			t.Setenv("ACCOUNT_DESCENDANT_READS_ENABLED", "1")
			r := newDescendantAccessRepo()
			r.members = map[string]*model.AccountMember{}
			r.member(ancestor, "reader", model.RoleOwner)
			meeting, permission, err := resolveSharedAccess(context.Background(), r, "reader", "meeting")
			if err != nil || meeting == nil || permission != model.PermissionRead || meeting.Content != "approved" {
				t.Fatalf("ancestor shared meeting = %v, %q, %v; want approved read", meeting != nil, permission, err)
			}
			delete(r.members, ancestor+"/reader")
			meeting, _, err = resolveSharedAccess(context.Background(), r, "reader", "meeting")
			if err != nil || meeting != nil {
				t.Fatalf("removed ancestor member retained access: %v/%v", meeting != nil, err)
			}
		})
	}
}

func TestAccountReadGrantRejectsPrivacyVersionChange(t *testing.T) {
	r := newDescendantAccessRepo()
	r.member("child", "reader", model.RoleAM)
	grant, err := resolveAccountReadGrant(context.Background(), r, "child", "reader")
	if err != nil || grant == nil {
		t.Fatalf("initial grant: %v/%v", grant, err)
	}
	r.members["child/reader"].SummaryPrivacyVersion = model.SummaryPrivacyVersion
	valid, err := revalidateAccountReadGrant(context.Background(), r, grant, "reader")
	if err != nil || valid {
		t.Fatalf("privacy version changed but grant remained valid: %v/%v", valid, err)
	}
}

func TestDescendantShareNeverEscalatesOrGrandfathers(t *testing.T) {
	t.Setenv("ACCOUNT_DESCENDANT_READS_ENABLED", "1")
	t.Setenv(SummaryPrivacyReaderEnv, "0")
	r := newDescendantAccessRepo()
	r.share = &model.Share{Origin: model.ShareOriginAccount, Permission: model.PermissionEdit}
	meeting, permission, err := resolveSharedAccess(context.Background(), r, "reader", "meeting")
	if err != nil || meeting == nil || permission != model.PermissionRead {
		t.Fatalf("inherited stale edit share = %v/%q/%v", meeting != nil, permission, err)
	}
	r.meeting.SummaryPrivacy = nil
	meeting, _, err = resolveSharedAccess(context.Background(), r, "reader", "meeting")
	if err != nil || meeting == nil || !meeting.SummaryReadRestricted || meeting.Content != "" {
		t.Fatalf("new child grant read legacy private derivatives: %+v/%v", meeting, err)
	}
	r.share.Origin = ""
	meeting, permission, err = resolveSharedAccess(context.Background(), r, "reader", "meeting")
	if err != nil || meeting == nil || permission != model.PermissionEdit || meeting.SummaryReadRestricted {
		t.Fatalf("independent grant lost its permission: %+v/%q/%v", meeting, permission, err)
	}
}

func TestDescendantReadRejectsInvalidPathsAndPrivateMeetings(t *testing.T) {
	for _, tc := range []string{"flag off", "private", "sibling", "upward", "missing", "cycle", "bad identity", "malformed parent", "too deep"} {
		t.Run(tc, func(t *testing.T) {
			t.Setenv("ACCOUNT_DESCENDANT_READS_ENABLED", "1")
			r := newDescendantAccessRepo()
			switch tc {
			case "flag off":
				t.Setenv("ACCOUNT_DESCENDANT_READS_ENABLED", "")
			case "private":
				r.meeting.SharedToAccount = false
			case "sibling":
				delete(r.members, "parent/reader")
				r.account("sibling", "parent")
				r.member("sibling", "reader", model.RoleAM)
			case "upward":
				delete(r.members, "parent/reader")
				r.member("child", "reader", model.RoleAM)
				r.meeting.AccountID = "parent"
			case "missing":
				delete(r.accounts, "root")
			case "cycle":
				r.accounts["root"].ParentAccountID = "child"
			case "bad identity":
				r.accounts["parent"].PK = "ACCOUNT#different"
			case "malformed parent":
				r.accounts["root"].ParentAccountID = "../other"
			case "too deep":
				r.accounts["root"].ParentAccountID = "a0"
				for i := 0; i < 64; i++ {
					parent := ""
					if i < 63 {
						parent = fmt.Sprintf("a%d", i+1)
					}
					r.account(fmt.Sprintf("a%d", i), parent)
				}
			}
			meeting, _, err := resolveSharedAccess(context.Background(), r, "reader", "meeting")
			if err != nil || meeting != nil {
				t.Fatalf("%s retained inherited meeting: %v/%v", tc, meeting != nil, err)
			}
		})
	}
}

func TestDescendantReadStorageFailureSurfaces(t *testing.T) {
	t.Setenv("ACCOUNT_DESCENDANT_READS_ENABLED", "1")
	r := newDescendantAccessRepo()
	r.err = errors.New("unavailable")
	_, _, err := resolveSharedAccess(context.Background(), r, "reader", "meeting")
	if !errors.Is(err, r.err) {
		t.Fatalf("read hid storage failure: %v", err)
	}
}
