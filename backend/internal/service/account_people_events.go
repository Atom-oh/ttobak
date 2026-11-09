package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// ValidatePeopleResolveRequested checks the IDs, that the owner and meeting
// are set together, and the reason.
func ValidatePeopleResolveRequested(e model.PeopleResolveRequested) error {
	if validateAccountHierarchyIDs(e.AccountID, "") != nil || (e.OwnerID == "") != (e.MeetingID == "") {
		return ErrInvalidInput
	}
	if e.OwnerID != "" && (validateAccountHierarchyIDs(e.OwnerID, "") != nil || validateAccountHierarchyIDs(e.MeetingID, "") != nil) {
		return ErrInvalidInput
	}
	switch e.Reason {
	case model.PeopleResolveReasonSpeakers, model.PeopleResolveReasonContacts:
		return nil
	}
	return ErrInvalidInput
}

// PeopleResolvePublisher publishes one PeopleResolveRequested event.
func PeopleResolvePublisher(client actionEventClient) func(context.Context, model.PeopleResolveRequested) error {
	return func(ctx context.Context, event model.PeopleResolveRequested) error {
		if err := ValidatePeopleResolveRequested(event); err != nil {
			return err
		}
		detail, err := json.Marshal(event)
		if err != nil {
			return err
		}
		out, err := client.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
			EventBusName: aws.String("default"), Source: aws.String("ttobak.analysis"),
			DetailType: aws.String("PeopleResolveRequested"), Detail: aws.String(string(detail)),
		}}})
		if err == nil && (out == nil || out.FailedEntryCount != 0 || len(out.Entries) != 1 || aws.ToString(out.Entries[0].EventId) == "") {
			err = errors.New("people resolve event was not accepted")
		}
		return err
	}
}

// ProcessPeopleResolveRequested runs one resolve request. A meeting request
// whose meeting is no longer in the account is skipped; a busy run returns
// nil because the lease holder reruns. Errors are returned for retry.
func (s *AccountPeopleService) ProcessPeopleResolveRequested(ctx context.Context, e model.PeopleResolveRequested) (PeopleRunResult, error) {
	if err := ValidatePeopleResolveRequested(e); err != nil {
		return PeopleRunResult{}, err
	}
	if e.MeetingID == "" {
		return s.ResolveAccount(ctx, e.AccountID, s.now().Add(-peopleBackfillWindow), maxPeopleBackfillMeetings, PeopleRunOptions{})
	}
	return s.resolveMeeting(ctx, e.OwnerID, e.MeetingID, e.AccountID, nil, PeopleRunOptions{})
}

// PeopleRunErrorCode maps a resolver error to a log code; error text may
// carry IDs or SDK detail and is never logged.
func PeopleRunErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	case errors.Is(err, ErrInvalidInput):
		return "invalid"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, repository.ErrContactEncryptionUnavailable):
		return "contacts_unavailable"
	case errors.Is(err, repository.ErrContactUnreadable):
		return "contact_unreadable"
	case errors.Is(err, repository.ErrConditionFailed):
		return "condition"
	}
	return "unknown"
}
