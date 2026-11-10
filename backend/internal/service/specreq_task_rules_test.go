package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

const (
	testSRSource = "1a1a1a1a-2b2b-4c3c-8d4d-5e5e5e5e5e5e"
	testSRReq    = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
)

func TestApplySpecReqTransition_Table(t *testing.T) {
	tests := []struct {
		name    string
		current model.SpecReqTask
		change  model.SpecReqTaskTransition
		state   string
		path    string
		counter string
		account string
	}{
		{"classify to needs_account", model.SpecReqTask{State: "needs_account"},
			model.SpecReqTaskTransition{Op: "classify", State: "needs_account"}, "needs_account", "", "", ""},
		{"classify to needs_owner", model.SpecReqTask{State: "needs_account"},
			model.SpecReqTaskTransition{Op: "classify", State: "needs_owner", AccountID: "acct1"}, "needs_owner", "", "", "acct1"},
		{"classify mine", model.SpecReqTask{State: "needs_owner", AccountID: "acct1"},
			model.SpecReqTaskTransition{Op: "classify", State: "issue_needed", Path: "mine"}, "issue_needed", "mine", "", "acct1"},
		{"classify theirs", model.SpecReqTask{State: "needs_account"},
			model.SpecReqTaskTransition{Op: "classify", State: "request_needed", Path: "theirs", AccountID: "acct1", CounterpartUserID: "u2"},
			"request_needed", "theirs", "u2", "acct1"},
		{"pickAccount from needs_account", model.SpecReqTask{State: "needs_account"},
			model.SpecReqTaskTransition{Op: "pickAccount", AccountID: "acct2"}, "needs_account", "", "", "acct2"},
		{"pickAccount from needs_owner clears", model.SpecReqTask{State: "needs_owner", AccountID: "acct1"},
			model.SpecReqTaskTransition{Op: "pickAccount", AccountID: "acct2"}, "needs_account", "", "", "acct2"},
		{"pickOwner", model.SpecReqTask{State: "needs_owner", AccountID: "acct1"},
			model.SpecReqTaskTransition{Op: "pickOwner", CounterpartUserID: "u2"}, "request_needed", "theirs", "u2", "acct1"},
		{"markRequested", model.SpecReqTask{State: "request_needed", Path: "theirs", AccountID: "acct1", CounterpartUserID: "u2"},
			model.SpecReqTaskTransition{Op: "markRequested"}, "requested", "theirs", "u2", "acct1"},
		{"link mine", model.SpecReqTask{State: "issue_needed", Path: "mine", AccountID: "acct1"},
			model.SpecReqTaskTransition{Op: "linkSpecReq", SpecReqID: testSRReq, SpecReqTitle: "t", SpecReqStatus: "PENDING"}, "issued", "mine", "", "acct1"},
		{"link theirs request_needed", model.SpecReqTask{State: "request_needed", Path: "theirs", AccountID: "acct1", CounterpartUserID: "u2"},
			model.SpecReqTaskTransition{Op: "linkSpecReq", SpecReqID: testSRReq}, "request_needed", "theirs", "u2", "acct1"},
		{"link theirs requested", model.SpecReqTask{State: "requested", Path: "theirs", AccountID: "acct1", CounterpartUserID: "u2"},
			model.SpecReqTaskTransition{Op: "linkSpecReq", SpecReqID: testSRReq}, "requested", "theirs", "u2", "acct1"},
		{"ack from request_needed", model.SpecReqTask{State: "request_needed", Path: "theirs", SpecReqID: testSRReq},
			model.SpecReqTaskTransition{Op: "recordAck", State: "acked", SpecReqStatus: "WORKING"}, "acked", "theirs", "", ""},
		{"ack from requested fails", model.SpecReqTask{State: "requested", Path: "theirs"},
			model.SpecReqTaskTransition{Op: "recordAck", State: "ack_failed", FailureCode: "ACK_REJECTED", SpecReqID: testSRReq}, "ack_failed", "theirs", "", ""},
		{"ack retry from ack_failed", model.SpecReqTask{State: "ack_failed", Path: "theirs", SpecReqID: testSRReq, FailureCode: "ACK_UNCONFIRMED"},
			model.SpecReqTaskTransition{Op: "recordAck", State: "acked"}, "acked", "theirs", "", ""},
		{"refresh acked completed", model.SpecReqTask{State: "acked", Path: "theirs", SpecReqID: testSRReq},
			model.SpecReqTaskTransition{Op: "refreshSpecReq", SpecReqStatus: "COMPLETED"}, "resolved", "theirs", "", ""},
		{"refresh ack_failed working", model.SpecReqTask{State: "ack_failed", Path: "theirs", SpecReqID: testSRReq, FailureCode: "ACK_REJECTED"},
			model.SpecReqTaskTransition{Op: "refreshSpecReq", SpecReqStatus: "WORKING"}, "acked", "theirs", "", ""},
		{"refresh mine title", model.SpecReqTask{State: "issued", Path: "mine", SpecReqID: testSRReq},
			model.SpecReqTaskTransition{Op: "refreshSpecReq", SpecReqTitle: "new", SpecReqStatus: "WAITING"}, "issued", "mine", "", ""},
		{"close", model.SpecReqTask{State: "requested", Path: "theirs"},
			model.SpecReqTaskTransition{Op: "close"}, "closed", "theirs", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applySpecReqTransition(tc.current, tc.change)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.State != tc.state || got.Path != tc.path || got.CounterpartUserID != tc.counter || got.AccountID != tc.account {
				t.Fatalf("got state=%q path=%q counterpart=%q account=%q", got.State, got.Path, got.CounterpartUserID, got.AccountID)
			}
			if got.Version != tc.current.Version {
				t.Fatalf("version changed: %d", got.Version)
			}
			if got.State == "acked" && got.FailureCode != "" {
				t.Fatalf("failure code kept: %q", got.FailureCode)
			}
		})
	}
}

func TestApplySpecReqTransition_SetsSpecReqFields(t *testing.T) {
	got, err := applySpecReqTransition(model.SpecReqTask{State: "issue_needed", Path: "mine"},
		model.SpecReqTaskTransition{Op: "linkSpecReq", SpecReqID: testSRReq, SpecReqTitle: "title", SpecReqStatus: "NEXT_UP"})
	if err != nil || got.SpecReqID != testSRReq || got.SpecReqTitle != "title" || got.SpecReqStatus != "NEXT_UP" {
		t.Fatalf("got %+v err=%v", got, err)
	}
	failed, err := applySpecReqTransition(model.SpecReqTask{State: "requested", Path: "theirs"},
		model.SpecReqTaskTransition{Op: "recordAck", State: "ack_failed", FailureCode: "ACK_NOT_ELIGIBLE", SpecReqID: testSRReq})
	if err != nil || failed.FailureCode != "ACK_NOT_ELIGIBLE" {
		t.Fatalf("got %+v err=%v", failed, err)
	}
}

func TestApplySpecReqTransition_RejectsOtherSources(t *testing.T) {
	tests := []struct {
		name    string
		current model.SpecReqTask
		change  model.SpecReqTaskTransition
	}{
		{"markRequested from requested", model.SpecReqTask{State: "requested", Path: "theirs"}, model.SpecReqTaskTransition{Op: "markRequested"}},
		{"classify from request_needed", model.SpecReqTask{State: "request_needed", Path: "theirs"},
			model.SpecReqTaskTransition{Op: "classify", State: "needs_account"}},
		{"pickOwner from needs_account", model.SpecReqTask{State: "needs_account"}, model.SpecReqTaskTransition{Op: "pickOwner", CounterpartUserID: "u2"}},
		{"pickAccount from issued", model.SpecReqTask{State: "issued", Path: "mine"}, model.SpecReqTaskTransition{Op: "pickAccount", AccountID: "a"}},
		{"link mine issued", model.SpecReqTask{State: "issued", Path: "mine"}, model.SpecReqTaskTransition{Op: "linkSpecReq", SpecReqID: testSRReq}},
		{"link theirs acked", model.SpecReqTask{State: "acked", Path: "theirs"}, model.SpecReqTaskTransition{Op: "linkSpecReq", SpecReqID: testSRReq}},
		{"ack on mine", model.SpecReqTask{State: "issue_needed", Path: "mine"},
			model.SpecReqTaskTransition{Op: "recordAck", State: "acked", SpecReqID: testSRReq}},
		{"ack from acked", model.SpecReqTask{State: "acked", Path: "theirs", SpecReqID: testSRReq}, model.SpecReqTaskTransition{Op: "recordAck", State: "acked"}},
		{"refresh without id", model.SpecReqTask{State: "requested", Path: "theirs"}, model.SpecReqTaskTransition{Op: "refreshSpecReq", SpecReqStatus: "WORKING"}},
		{"refresh closed", model.SpecReqTask{State: "closed", Path: "theirs", SpecReqID: testSRReq}, model.SpecReqTaskTransition{Op: "refreshSpecReq", SpecReqStatus: "WORKING"}},
		{"close closed", model.SpecReqTask{State: "closed"}, model.SpecReqTaskTransition{Op: "close"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := applySpecReqTransition(tc.current, tc.change); !errors.Is(err, ErrConflict) {
				t.Fatalf("got %v, want ErrConflict", err)
			}
		})
	}
}

func TestTransition_ClassifyRejectedAfterPick(t *testing.T) {
	current := model.SpecReqTask{State: "request_needed", Path: "theirs", AccountID: "acct1", CounterpartUserID: "u2"}
	_, err := applySpecReqTransition(current, model.SpecReqTaskTransition{
		Op: "classify", State: "request_needed", Path: "theirs", AccountID: "acct1", CounterpartUserID: "u3"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("got %v, want ErrConflict", err)
	}
}

func TestApplySpecReqTransition_ResolvedOnlyOnCompleted(t *testing.T) {
	acked := model.SpecReqTask{State: "acked", Path: "theirs", SpecReqID: testSRReq}
	got, err := applySpecReqTransition(acked, model.SpecReqTaskTransition{Op: "refreshSpecReq", SpecReqStatus: "CANCELLED"})
	if err != nil || got.State != "acked" || got.SpecReqStatus != "CANCELLED" {
		t.Fatalf("cancelled: %+v err=%v", got, err)
	}
	got, err = applySpecReqTransition(acked, model.SpecReqTaskTransition{Op: "refreshSpecReq", SpecReqStatus: "COMPLETED"})
	if err != nil || got.State != "resolved" {
		t.Fatalf("completed: %+v err=%v", got, err)
	}
	mine := model.SpecReqTask{State: "issued", Path: "mine", SpecReqID: testSRReq}
	got, err = applySpecReqTransition(mine, model.SpecReqTaskTransition{Op: "refreshSpecReq", SpecReqStatus: "COMPLETED"})
	if err != nil || got.State != "issued" || got.SpecReqStatus != "COMPLETED" {
		t.Fatalf("mine: %+v err=%v", got, err)
	}
	failed := model.SpecReqTask{State: "ack_failed", Path: "theirs", SpecReqID: testSRReq, FailureCode: "ACK_REJECTED"}
	got, err = applySpecReqTransition(failed, model.SpecReqTaskTransition{Op: "refreshSpecReq", SpecReqStatus: "PENDING"})
	if err != nil || got.State != "ack_failed" || got.FailureCode != "ACK_REJECTED" {
		t.Fatalf("ack_failed pending: %+v err=%v", got, err)
	}
}

func TestApplySpecReqTransition_RejectsInvalidInput(t *testing.T) {
	needsOwner := model.SpecReqTask{State: "needs_owner", AccountID: "acct1"}
	requested := model.SpecReqTask{State: "requested", Path: "theirs"}
	tests := []struct {
		name    string
		current model.SpecReqTask
		change  model.SpecReqTaskTransition
	}{
		{"unknown op", needsOwner, model.SpecReqTaskTransition{Op: "nope"}},
		{"empty op", needsOwner, model.SpecReqTaskTransition{}},
		{"unknown path", needsOwner, model.SpecReqTaskTransition{Op: "classify", State: "issue_needed", Path: "ours"}},
		{"unknown state", needsOwner, model.SpecReqTaskTransition{Op: "classify", State: "bogus"}},
		{"unknown failure code", requested, model.SpecReqTaskTransition{Op: "recordAck", State: "ack_failed", FailureCode: "X", SpecReqID: testSRReq}},
		{"ack_failed without code", requested, model.SpecReqTaskTransition{Op: "recordAck", State: "ack_failed", SpecReqID: testSRReq}},
		{"acked with code", requested, model.SpecReqTaskTransition{Op: "recordAck", State: "acked", FailureCode: "ACK_REJECTED", SpecReqID: testSRReq}},
		{"ack without specreq id", requested, model.SpecReqTaskTransition{Op: "recordAck", State: "acked"}},
		{"ack wrong target state", requested, model.SpecReqTaskTransition{Op: "recordAck", State: "resolved", SpecReqID: testSRReq}},
		{"bad specreq id", model.SpecReqTask{State: "issue_needed", Path: "mine"}, model.SpecReqTaskTransition{Op: "linkSpecReq", SpecReqID: "nope"}},
		{"link without id", model.SpecReqTask{State: "issue_needed", Path: "mine"}, model.SpecReqTaskTransition{Op: "linkSpecReq"}},
		{"bad status", model.SpecReqTask{State: "issued", Path: "mine", SpecReqID: testSRReq}, model.SpecReqTaskTransition{Op: "refreshSpecReq", SpecReqStatus: "DONE"}},
		{"long title", model.SpecReqTask{State: "issued", Path: "mine", SpecReqID: testSRReq},
			model.SpecReqTaskTransition{Op: "refreshSpecReq", SpecReqTitle: strings.Repeat("a", 513)}},
		{"invalid utf8 title", model.SpecReqTask{State: "issued", Path: "mine", SpecReqID: testSRReq},
			model.SpecReqTaskTransition{Op: "refreshSpecReq", SpecReqTitle: "\xff"}},
		{"refresh other id", model.SpecReqTask{State: "issued", Path: "mine", SpecReqID: testSRReq},
			model.SpecReqTaskTransition{Op: "refreshSpecReq", SpecReqID: "bbbbbbbb-bbbb-4ccc-8ddd-eeeeeeeeeeee"}},
		{"bad account", model.SpecReqTask{State: "needs_account"}, model.SpecReqTaskTransition{Op: "pickAccount", AccountID: "a b"}},
		{"pickAccount empty", model.SpecReqTask{State: "needs_account"}, model.SpecReqTaskTransition{Op: "pickAccount"}},
		{"pickOwner empty", needsOwner, model.SpecReqTaskTransition{Op: "pickOwner"}},
		{"bad counterpart", needsOwner, model.SpecReqTaskTransition{Op: "pickOwner", CounterpartUserID: "u 2"}},
		{"classify needs_owner without account", model.SpecReqTask{State: "needs_account"}, model.SpecReqTaskTransition{Op: "classify", State: "needs_owner"}},
		{"classify theirs without counterpart", needsOwner, model.SpecReqTaskTransition{Op: "classify", State: "request_needed", Path: "theirs"}},
		{"classify non-initial state", needsOwner, model.SpecReqTaskTransition{Op: "classify", State: "issued", Path: "mine"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := applySpecReqTransition(tc.current, tc.change); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("got %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestApplySpecReqTransition_DoesNotMutateInput(t *testing.T) {
	current := model.SpecReqTask{State: "needs_owner", AccountID: "acct1", Version: 4}
	if _, err := applySpecReqTransition(current, model.SpecReqTaskTransition{Op: "pickOwner", CounterpartUserID: "u2"}); err != nil {
		t.Fatal(err)
	}
	if current.State != "needs_owner" || current.CounterpartUserID != "" || current.Version != 4 {
		t.Fatalf("input mutated: %+v", current)
	}
}

func TestValidateSpecReqCreate(t *testing.T) {
	base := func(mod func(*model.CreateSpecReqTaskRequest)) model.CreateSpecReqTaskRequest {
		req := model.CreateSpecReqTaskRequest{SourceID: testSRSource, ExternalID: "ext-1", State: "needs_account"}
		mod(&req)
		return req
	}
	valid := []model.CreateSpecReqTaskRequest{
		base(func(r *model.CreateSpecReqTaskRequest) {}),
		base(func(r *model.CreateSpecReqTaskRequest) { r.State, r.AccountID = "needs_owner", "acct1" }),
		base(func(r *model.CreateSpecReqTaskRequest) {
			r.State, r.AccountID, r.Path = "issue_needed", "acct1", "mine"
		}),
		base(func(r *model.CreateSpecReqTaskRequest) {
			r.State, r.AccountID, r.Path, r.CounterpartUserID = "request_needed", "acct1", "theirs", "u2"
		}),
		base(func(r *model.CreateSpecReqTaskRequest) { r.ExternalID = strings.Repeat("a", 512) }),
	}
	for i, req := range valid {
		if err := validateSpecReqCreate(req); err != nil {
			t.Errorf("valid[%d]: %v", i, err)
		}
	}
	invalid := map[string]model.CreateSpecReqTaskRequest{
		"513-byte external id":       base(func(r *model.CreateSpecReqTaskRequest) { r.ExternalID = strings.Repeat("a", 513) }),
		"blank external id":          base(func(r *model.CreateSpecReqTaskRequest) { r.ExternalID = "  " }),
		"non-uuid source":            base(func(r *model.CreateSpecReqTaskRequest) { r.SourceID = "not-a-uuid" }),
		"uppercase source":           base(func(r *model.CreateSpecReqTaskRequest) { r.SourceID = strings.ToUpper(testSRSource) }),
		"needs_account with account": base(func(r *model.CreateSpecReqTaskRequest) { r.AccountID = "acct1" }),
		"needs_owner no account":     base(func(r *model.CreateSpecReqTaskRequest) { r.State = "needs_owner" }),
		"needs_owner with path": base(func(r *model.CreateSpecReqTaskRequest) {
			r.State, r.AccountID, r.Path = "needs_owner", "acct1", "mine"
		}),
		"issue_needed theirs": base(func(r *model.CreateSpecReqTaskRequest) {
			r.State, r.AccountID, r.Path = "issue_needed", "acct1", "theirs"
		}),
		"issue_needed no account": base(func(r *model.CreateSpecReqTaskRequest) { r.State, r.Path = "issue_needed", "mine" }),
		"request_needed no counterpart": base(func(r *model.CreateSpecReqTaskRequest) {
			r.State, r.AccountID, r.Path = "request_needed", "acct1", "theirs"
		}),
		"request_needed mine": base(func(r *model.CreateSpecReqTaskRequest) {
			r.State, r.AccountID, r.Path, r.CounterpartUserID = "request_needed", "acct1", "mine", "u2"
		}),
		"non-initial state": base(func(r *model.CreateSpecReqTaskRequest) {
			r.State, r.AccountID, r.Path = "issued", "acct1", "mine"
		}),
		"unknown path":   base(func(r *model.CreateSpecReqTaskRequest) { r.Path = "ours" }),
		"bad account id": base(func(r *model.CreateSpecReqTaskRequest) { r.State, r.AccountID = "needs_owner", "a/b" }),
	}
	for name, req := range invalid {
		if err := validateSpecReqCreate(req); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: got %v, want ErrInvalidInput", name, err)
		}
	}
}
