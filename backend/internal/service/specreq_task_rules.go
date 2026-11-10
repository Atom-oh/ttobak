package service

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ttobak/backend/internal/model"
)

const specReqTextMaxBytes = 512

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func validSpecReqTaskState(state string) bool {
	switch state {
	case model.SpecReqStateIssueNeeded, model.SpecReqStateIssued, model.SpecReqStateRequestNeeded,
		model.SpecReqStateRequested, model.SpecReqStateAcked, model.SpecReqStateAckFailed,
		model.SpecReqStateResolved, model.SpecReqStateNeedsAccount, model.SpecReqStateNeedsOwner,
		model.SpecReqStateClosed:
		return true
	}
	return false
}

func validSpecReqTaskPath(path string) bool {
	return path == model.SpecReqPathMine || path == model.SpecReqPathTheirs
}

func validSpecReqFailureCode(code string) bool {
	switch code {
	case model.SpecReqFailAckRejected, model.SpecReqFailAckUnconfirmed, model.SpecReqFailAckNotEligible:
		return true
	}
	return false
}

// validSpecReqInitial checks the state/path/account/counterpart combinations
// shared by creation and classification.
func validSpecReqInitial(state, path, accountID, counterpart string) bool {
	switch state {
	case model.SpecReqStateNeedsAccount:
		return accountID == "" && path == "" && counterpart == ""
	case model.SpecReqStateNeedsOwner:
		return accountID != "" && path == "" && counterpart == ""
	case model.SpecReqStateIssueNeeded:
		return accountID != "" && path == model.SpecReqPathMine && counterpart == ""
	case model.SpecReqStateRequestNeeded:
		return accountID != "" && path == model.SpecReqPathTheirs && counterpart != ""
	}
	return false
}

func validSpecReqText(value string) bool {
	return len(value) <= specReqTextMaxBytes && utf8.ValidString(value)
}

func validateSpecReqCreate(req model.CreateSpecReqTaskRequest) error {
	if !canonicalUUID(req.SourceID) ||
		strings.TrimSpace(req.ExternalID) == "" || len(req.ExternalID) > specReqTextMaxBytes || !utf8.ValidString(req.ExternalID) {
		return ErrInvalidInput
	}
	if req.AccountID != "" && !calendarIdentifier.MatchString(req.AccountID) ||
		req.CounterpartUserID != "" && !calendarIdentifier.MatchString(req.CounterpartUserID) ||
		req.Path != "" && !validSpecReqTaskPath(req.Path) {
		return ErrInvalidInput
	}
	if !validSpecReqInitial(req.State, req.Path, req.AccountID, req.CounterpartUserID) {
		return ErrInvalidInput
	}
	return nil
}

// validateSpecReqTransitionFields rejects malformed values in any op.
func validateSpecReqTransitionFields(change model.SpecReqTaskTransition) error {
	switch {
	case change.AccountID != "" && !calendarIdentifier.MatchString(change.AccountID),
		change.CounterpartUserID != "" && !calendarIdentifier.MatchString(change.CounterpartUserID),
		change.Path != "" && !validSpecReqTaskPath(change.Path),
		change.State != "" && !validSpecReqTaskState(change.State),
		change.SpecReqID != "" && !canonicalUUID(change.SpecReqID),
		!validSpecReqText(change.SpecReqTitle),
		change.SpecReqStatus != "" && !slices.Contains(model.SpecReqStatuses, change.SpecReqStatus),
		change.FailureCode != "" && !validSpecReqFailureCode(change.FailureCode):
		return ErrInvalidInput
	}
	return nil
}

// applySpecReqTransition returns the row after one transition. It is pure:
// the repository increments Version, sets UpdatedAt, and sets StateChangedAt
// only when the returned State differs from current.State.
func applySpecReqTransition(current model.SpecReqTask, change model.SpecReqTaskTransition) (model.SpecReqTask, error) {
	switch change.Op {
	case model.SpecReqOpClassify, model.SpecReqOpPickAccount, model.SpecReqOpPickOwner,
		model.SpecReqOpMarkRequested, model.SpecReqOpLinkSpecReq, model.SpecReqOpRecordAck,
		model.SpecReqOpRefreshSpecReq, model.SpecReqOpClose:
	default:
		return model.SpecReqTask{}, ErrInvalidInput
	}
	if err := validateSpecReqTransitionFields(change); err != nil {
		return model.SpecReqTask{}, err
	}
	switch change.Op {
	case model.SpecReqOpLinkSpecReq, model.SpecReqOpRecordAck, model.SpecReqOpRefreshSpecReq:
		// A linked SpecReq is pinned: another ID would inherit the previous
		// request's title and status.
		if current.SpecReqID != "" && change.SpecReqID != "" && change.SpecReqID != current.SpecReqID {
			return model.SpecReqTask{}, ErrConflict
		}
	}
	next := current
	needsPick := current.State == model.SpecReqStateNeedsAccount || current.State == model.SpecReqStateNeedsOwner
	switch change.Op {
	case model.SpecReqOpClassify:
		if !needsPick {
			return model.SpecReqTask{}, ErrConflict
		}
		accountID := change.AccountID
		if accountID == "" {
			accountID = current.AccountID
		}
		if !validSpecReqInitial(change.State, change.Path, accountID, change.CounterpartUserID) {
			return model.SpecReqTask{}, ErrInvalidInput
		}
		next.State, next.AccountID, next.Path, next.CounterpartUserID = change.State, accountID, change.Path, change.CounterpartUserID
	case model.SpecReqOpPickAccount:
		if !needsPick {
			return model.SpecReqTask{}, ErrConflict
		}
		if change.AccountID == "" {
			return model.SpecReqTask{}, ErrInvalidInput
		}
		next.State, next.AccountID, next.Path, next.CounterpartUserID = model.SpecReqStateNeedsAccount, change.AccountID, "", ""
	case model.SpecReqOpPickOwner:
		if current.State != model.SpecReqStateNeedsOwner {
			return model.SpecReqTask{}, ErrConflict
		}
		if change.CounterpartUserID == "" {
			return model.SpecReqTask{}, ErrInvalidInput
		}
		next.State, next.Path, next.CounterpartUserID = model.SpecReqStateRequestNeeded, model.SpecReqPathTheirs, change.CounterpartUserID
	case model.SpecReqOpMarkRequested:
		if current.State != model.SpecReqStateRequestNeeded {
			return model.SpecReqTask{}, ErrConflict
		}
		next.State = model.SpecReqStateRequested
	case model.SpecReqOpLinkSpecReq:
		mine := current.Path == model.SpecReqPathMine && current.State == model.SpecReqStateIssueNeeded
		theirs := current.Path == model.SpecReqPathTheirs &&
			(current.State == model.SpecReqStateRequestNeeded || current.State == model.SpecReqStateRequested)
		if !mine && !theirs {
			return model.SpecReqTask{}, ErrConflict
		}
		if change.SpecReqID == "" {
			return model.SpecReqTask{}, ErrInvalidInput
		}
		if mine {
			next.State = model.SpecReqStateIssued
		}
		setSpecReqFields(&next, change)
	case model.SpecReqOpRecordAck:
		if current.Path != model.SpecReqPathTheirs || current.State != model.SpecReqStateRequestNeeded &&
			current.State != model.SpecReqStateRequested && current.State != model.SpecReqStateAckFailed {
			return model.SpecReqTask{}, ErrConflict
		}
		setSpecReqFields(&next, change)
		if next.SpecReqID == "" {
			return model.SpecReqTask{}, ErrInvalidInput
		}
		switch {
		case change.State == model.SpecReqStateAcked && change.FailureCode == "":
			next.State, next.FailureCode = model.SpecReqStateAcked, ""
		case change.State == model.SpecReqStateAckFailed && change.FailureCode != "":
			next.State, next.FailureCode = model.SpecReqStateAckFailed, change.FailureCode
		default:
			return model.SpecReqTask{}, ErrInvalidInput
		}
	case model.SpecReqOpRefreshSpecReq:
		if current.SpecReqID == "" || current.State == model.SpecReqStateClosed {
			return model.SpecReqTask{}, ErrConflict
		}
		// Promotion needs a freshly observed status, never the stored one.
		if change.SpecReqStatus == "" {
			return model.SpecReqTask{}, ErrInvalidInput
		}
		setSpecReqFields(&next, change)
		if current.Path == model.SpecReqPathTheirs {
			switch {
			case current.State == model.SpecReqStateAcked && change.SpecReqStatus == model.SpecReqStatusCompleted:
				next.State = model.SpecReqStateResolved
			case current.State == model.SpecReqStateAckFailed && (change.SpecReqStatus == model.SpecReqStatusWorking ||
				change.SpecReqStatus == model.SpecReqStatusWaiting || change.SpecReqStatus == model.SpecReqStatusCompleted):
				next.State, next.FailureCode = model.SpecReqStateAcked, ""
			}
		}
	case model.SpecReqOpClose:
		if current.State == model.SpecReqStateClosed {
			return model.SpecReqTask{}, ErrConflict
		}
		next.State = model.SpecReqStateClosed
	}
	return next, nil
}

func setSpecReqFields(task *model.SpecReqTask, change model.SpecReqTaskTransition) {
	if change.SpecReqID != "" {
		task.SpecReqID = change.SpecReqID
	}
	if change.SpecReqTitle != "" {
		task.SpecReqTitle = change.SpecReqTitle
	}
	if change.SpecReqStatus != "" {
		task.SpecReqStatus = change.SpecReqStatus
	}
}
