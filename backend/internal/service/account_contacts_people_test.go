package service

import (
	"context"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestContactCreate_PublishesAccountResolve(t *testing.T) {
	repo := &contactTestRepo{account: true, member: true}
	s := NewAccountContactsService(repo)
	var got []model.PeopleResolveRequested
	s.SetPeopleResolvePublisher(func(_ context.Context, e model.PeopleResolveRequested) error { got = append(got, e); return nil })
	created, err := s.Mutate(context.Background(), "acc", "u1", "create", "", model.AccountContact{Name: "Kim"})
	if err != nil || len(got) != 1 || got[0] != (model.PeopleResolveRequested{AccountID: "acc", Reason: model.PeopleResolveReasonContacts}) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	// A bulk import defers each row and requests one resolve at its end.
	if _, err := s.MutateWith(context.Background(), "acc", "u1", "update", created.ContactID,
		model.AccountContact{Name: "Kim", Revision: created.Revision}, ContactMutateOptions{DeferPeopleResolve: true}); err != nil || len(got) != 1 {
		t.Fatalf("deferred got=%+v err=%v", got, err)
	}
	if _, err := s.Mutate(context.Background(), "acc", "u1", "delete", created.ContactID, model.AccountContact{Revision: created.Revision}); err != nil || len(got) != 1 {
		t.Fatalf("delete got=%+v err=%v", got, err)
	}
}
