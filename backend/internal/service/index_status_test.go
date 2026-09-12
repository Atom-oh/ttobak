package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

type statusRepo struct {
	records map[string]*model.IndexRecord
	share   *model.Share
	member  *model.AccountMember
	reads   []string
}

func (r *statusRepo) GetIndexSource(_ context.Context, key model.IndexResource) (*model.IndexRecord, error) {
	r.reads = append(r.reads, key.PK)
	return r.records[key.PK], nil
}
func (r *statusRepo) GetDocShare(context.Context, string, string) (*model.Share, error) {
	return r.share, nil
}
func (r *statusRepo) GetMember(context.Context, string, string) (*model.AccountMember, error) {
	return r.member, nil
}

type statusReader struct {
	calls int
	key   model.IndexResource
	job   *model.IndexJob
}

func (r *statusReader) Status(_ context.Context, key model.IndexResource) (*model.IndexJob, error) {
	r.calls++
	r.key = key
	return r.job, nil
}

func TestIndexStatusPersonalShareRechecksGrantBeforeReadingSource(t *testing.T) {
	share := &model.Share{PK: "USER#viewer", SK: model.PrefixDocShare + "doc",
		MeetingID: "doc", OwnerID: "owner", SharedToID: "viewer", Permission: model.PermissionRead, EntityType: model.EntityTypeDocShare}
	for _, scenario := range []string{"valid", "revoked", "wrong recipient", "meeting share", "wrong owner field"} {
		t.Run(scenario, func(t *testing.T) {
			copy := *share
			repo := &statusRepo{share: &copy, records: map[string]*model.IndexRecord{
				"USER#owner": {Fields: map[string]interface{}{"docId": "doc", "sourceUserId": "owner"}},
			}}
			switch scenario {
			case "revoked":
				repo.share = nil
			case "wrong recipient":
				copy.SharedToID = "someone"
			case "meeting share":
				copy.EntityType = "SHARE"
			case "wrong owner field":
				repo.records["USER#owner"].Fields["sourceUserId"] = "foreign"
			}
			reader := &statusReader{job: &model.IndexJob{State: model.IndexIndexed}}
			service := &IndexStatusService{repo: repo, reader: reader}
			result, err := service.PersonalDocument(context.Background(), "viewer", "doc")
			if scenario == "valid" {
				if err != nil || result.State != model.IndexIndexed || reader.calls != 1 || reader.key.PK != "USER#owner" {
					t.Fatalf("valid share: %+v %v", result, err)
				}
			} else if !errors.Is(err, ErrNotFound) || reader.calls != 0 {
				t.Fatalf("invalid grant reached index/S3 reads: %+v %v calls=%d", result, err, reader.calls)
			}
		})
	}
}

func TestIndexStatusAccountMembershipPrecedesSourceRead(t *testing.T) {
	repo := &statusRepo{records: map[string]*model.IndexRecord{
		"ACCOUNT#team": {Fields: map[string]interface{}{"docId": "doc", "accountId": "team"}},
	}}
	reader := &statusReader{}
	service := &IndexStatusService{repo: repo, reader: reader}
	if _, err := service.AccountDocument(context.Background(), "viewer", "team", "doc"); !errors.Is(err, ErrForbidden) || len(repo.reads) != 0 || reader.calls != 0 {
		t.Fatalf("nonmember read: %v", err)
	}
	repo.member = &model.AccountMember{AccountID: "parent", UserID: "viewer"}
	if _, err := service.AccountDocument(context.Background(), "viewer", "team", "doc"); !errors.Is(err, ErrForbidden) || len(repo.reads) != 0 {
		t.Fatalf("wrong membership read: %v", err)
	}
	repo.member.AccountID = "team"
	result, err := service.AccountDocument(context.Background(), "viewer", "team", "doc")
	if err != nil || result.State != "UNTRACKED" || reader.calls != 1 {
		t.Fatalf("member read: %+v %v", result, err)
	}
}

func TestIndexStatusMeetingAuthorizationAndPrivateFieldProjection(t *testing.T) {
	reader := &statusReader{job: &model.IndexJob{
		State: model.IndexPending, ErrorCode: "OLD_FAILURE", UpdatedAt: 1000,
		Keys: []string{"private/key"}, PendingKeys: []string{"private/pending"}, SyncID: "provider-id", RunID: "private-run",
		Resource: model.IndexResource{PK: "USER#owner", SK: "MEETING#meeting"},
	}}
	allowed := false
	service := &IndexStatusService{reader: reader, meetingAccess: func(context.Context, string, string) (*model.Meeting, error) {
		if !allowed {
			return nil, ErrForbidden
		}
		return &model.Meeting{UserID: "owner", MeetingID: "meeting"}, nil
	}}
	if _, err := service.Meeting(context.Background(), "viewer", "meeting"); !errors.Is(err, ErrForbidden) || reader.calls != 0 {
		t.Fatalf("unauthorized status read: %v", err)
	}
	allowed = true
	result, err := service.Meeting(context.Background(), "viewer", "meeting")
	if err != nil || result.ErrorCode != "" {
		t.Fatalf("pending leaked an old failure: %+v %v", result, err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private", "provider-id", "USER#", "MEETING#"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("private job field leaked: %s", data)
		}
	}
}
