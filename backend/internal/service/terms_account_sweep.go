package service

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/aws/smithy-go"
	"github.com/ttobak/backend/internal/repository"
)

// termsAccountSweepTimeout bounds one relink sweep: at most
// MaxTermsAccepted+MaxTermsAccountAccepted keys per stale Account.
const termsAccountSweepTimeout = 10 * time.Second

// termsAccountSweeper is the ADR-008 FU4 relink sweep
// (repository.SweepMeetingAccountTerms).
type termsAccountSweeper interface {
	SweepMeetingAccountTerms(ctx context.Context, ownerID, meetingID string) (repository.TermsAccountSweep, error)
}

var _ termsAccountSweeper = (*repository.DynamoDBRepository)(nil)

// sweepUnlinkedAccountTerms removes the meeting's automatic-term evidence
// from recorded Accounts it is no longer linked to, after the caller's
// accountId change committed. It is best effort for the caller: the link
// change stands, so a failure is logged (trigger, meeting, codes and counts
// only) and returned for callers that count it; the sweep is idempotent and
// re-runs on the next link call, terms run or account merge pass, and meeting
// deletion cleans every recorded Account. It runs detached from the caller's
// cancellation, bounded by termsAccountSweepTimeout. repo without the
// sweeper is a no-op.
func sweepUnlinkedAccountTerms(ctx context.Context, repo any, trigger, ownerID, meetingID string) error {
	sweeper, ok := repo.(termsAccountSweeper)
	if !ok || ownerID == "" || meetingID == "" {
		return nil
	}
	sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), termsAccountSweepTimeout)
	defer cancel()
	result, err := sweeper.SweepMeetingAccountTerms(sweepCtx, ownerID, meetingID)
	if err == nil && result.Stale == 0 {
		return nil
	}
	outcome, code := "SWEPT", ""
	if err != nil {
		outcome, code = "FAILED", termsSweepErrorCode(err)
	}
	log.Printf("terms account sweep: trigger=%s meeting=%s outcome=%s code=%s stale=%d dropped=%d retained=%d",
		trigger, meetingID, outcome, code, result.Stale, result.Dropped, result.Retained)
	return err
}

// termsSweepErrorCode names a sweep failure without its message: the first
// AWS API error code, CONFLICT for a guard that kept failing, TIMEOUT, or
// ERROR.
func termsSweepErrorCode(err error) string {
	var apiErr smithy.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.ErrorCode() != "":
		return apiErr.ErrorCode()
	case errors.Is(err, repository.ErrConditionFailed):
		return "CONFLICT"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "TIMEOUT"
	}
	return "ERROR"
}

// hasStaleTermsAccount reports a recorded Account other than current.
func hasStaleTermsAccount(recorded []string, current string) bool {
	for _, id := range recorded {
		if id != "" && id != current {
			return true
		}
	}
	return false
}
