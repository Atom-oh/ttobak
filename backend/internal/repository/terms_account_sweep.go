package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// termsAccountPruneAttempts bounds the re-reads of one Account term whose
// version keeps changing under the relink sweep.
const termsAccountPruneAttempts = 3

// TermsAccountSweep counts one SweepMeetingAccountTerms call: Stale recorded
// Accounts the meeting no longer belongs to, Dropped of those cleaned and
// removed from accountIds, Retained cleaned but kept because the run state or
// the meeting's link changed during the sweep.
type TermsAccountSweep struct {
	Stale, Dropped, Retained int
}

// SweepMeetingAccountTerms is the ADR-008 FU4 relink sweep. It runs after a
// change of the meeting's accountId committed and cleans every Account
// recorded on the run state (accountIds) that the meeting is no longer linked
// to: per AccountSweepKeys key it deletes the meeting's DICTOBS# row, then
// deletes an unedited auto term left without observations or removes the
// meeting's `sources` entry (and a matching firstSourceMeetingId) from a term
// that stays, both version-guarded. Like the deletion sweep this is system
// cleanup of the meeting's own evidence and carries no membership check. Only
// a fully clean Account is removed from accountIds, conditioned on the run
// state's accountRecords being unchanged and on the meeting not being linked
// to it again. RecordTermsAccount and every Account observation write bump
// accountRecords, and an observation write also needs the Account in
// accountIds (withSourceMeetingCheck): a write after this sweep read the
// state keeps the entry, and none can land after the entry is gone. Every
// step is idempotent, so a failed or retained
// Account is retried by the next sweep or by meeting deletion. Only the owner
// recorded on the run state is swept; a deleted meeting is left to
// SweepMeetingTerms.
func (r *DynamoDBRepository) SweepMeetingAccountTerms(ctx context.Context, ownerID, meetingID string) (TermsAccountSweep, error) {
	var out TermsAccountSweep
	if ownerID == "" || meetingID == "" {
		return out, fmt.Errorf("terms account sweep requires owner and meeting")
	}
	state, err := r.GetTermsAnalysis(ctx, meetingID)
	if err != nil || state == nil || state.OwnerID != ownerID || len(state.AccountIDs) == 0 {
		return out, err
	}
	current, exists, err := r.meetingAccountLink(ctx, ownerID, meetingID)
	if err != nil || !exists {
		return out, err
	}
	keys := state.AccountSweepKeys()
	var errs []error
	for _, accountID := range state.AccountIDs {
		if accountID == "" || accountID == current {
			continue
		}
		out.Stale++
		if err := r.cleanAccountContribution(ctx, model.PrefixAccount+accountID, meetingID, keys); err != nil {
			errs = append(errs, err)
			continue
		}
		dropped, err := r.dropTermsAccount(ctx, ownerID, meetingID, accountID, state.AccountRecords)
		switch {
		case err != nil:
			errs = append(errs, err)
		case dropped:
			out.Dropped++
		default:
			out.Retained++
		}
	}
	return out, errors.Join(errs...)
}

// meetingAccountLink reads the meeting's current accountId consistently;
// exists is false when the meeting item is gone.
func (r *DynamoDBRepository) meetingAccountLink(ctx context.Context, ownerID, meetingID string) (accountID string, exists bool, err error) {
	projection, err := expression.NewBuilder().WithProjection(expression.NamesList(expression.Name("PK"), expression.Name("accountId"))).Build()
	if err != nil {
		return "", false, err
	}
	got, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: meetingKey(ownerID, meetingID), ConsistentRead: aws.Bool(true),
		ProjectionExpression: projection.Projection(), ExpressionAttributeNames: projection.Names(),
	})
	if err != nil {
		return "", false, fmt.Errorf("read meeting account: %w", err)
	}
	if len(got.Item) == 0 {
		return "", false, nil
	}
	if v, ok := got.Item["accountId"].(*types.AttributeValueMemberS); ok {
		return v.Value, true, nil
	}
	return "", true, nil
}

// cleanAccountContribution removes meetingID's evidence for keys from one
// Account partition and joins the failures.
func (r *DynamoDBRepository) cleanAccountContribution(ctx context.Context, pk, meetingID string, keys []string) error {
	var errs []error
	for _, key := range keys {
		if !model.ValidTermKey(key) {
			continue
		}
		if _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(r.tableName), Key: dictKey(pk, model.DictObsSK(key, meetingID)),
		}); err != nil {
			errs = append(errs, fmt.Errorf("delete account term observation: %w", err))
			continue
		}
		if err := r.pruneUnlinkedAccountTerm(ctx, pk, key, meetingID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// pruneUnlinkedAccountTerm deletes the term when it is an unedited auto term
// without observations, otherwise removes meetingID from its sources and
// firstSourceMeetingId. The version is read before the observation count (as
// in deleteOrphanAutoTerm) and every write is guarded by it; a lost guard
// re-reads, because a concurrent merge for another source rewrites sources
// from a copy that still names this meeting.
func (r *DynamoDBRepository) pruneUnlinkedAccountTerm(ctx context.Context, pk, termKey, meetingID string) error {
	for attempt := 0; attempt < termsAccountPruneAttempts; attempt++ {
		term, err := r.readSweptTerm(ctx, pk, termKey)
		if err != nil || term == nil || term.State == model.TermStateDeleted {
			return err
		}
		if autoManagedTerm(term) {
			observed, err := r.termHasObservations(ctx, pk, termKey)
			if err != nil {
				return err
			}
			if !observed {
				conflict, err := r.deleteOrphanAt(ctx, pk, termKey, term.Version)
				if err != nil || !conflict {
					return err
				}
				continue
			}
		}
		sources, changed := withoutMeetingSource(term.Sources, meetingID)
		first := term.FirstSourceMeetingID == meetingID
		if !changed && !first {
			return nil
		}
		update := expression.Set(expression.Name("version"), expression.Name("version").Plus(expression.Value(1)))
		switch {
		case changed && len(sources) == 0:
			update = update.Remove(expression.Name("sources"))
		case changed:
			update = update.Set(expression.Name("sources"), expression.Value(sources))
		}
		if first {
			update = update.Remove(expression.Name("firstSourceMeetingId"))
		}
		expr, err := expression.NewBuilder().WithUpdate(update).
			WithCondition(expression.Name("version").Equal(expression.Value(term.Version))).Build()
		if err != nil {
			return err
		}
		_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: aws.String(r.tableName), Key: dictKey(pk, model.DictTermSK(termKey)),
			UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		})
		var failed *types.ConditionalCheckFailedException
		if errors.As(err, &failed) {
			continue
		}
		if err != nil {
			return fmt.Errorf("remove account term source: %w", err)
		}
		return nil
	}
	return fmt.Errorf("prune account term: %w", ErrConditionFailed)
}

// withoutMeetingSource returns refs without the meeting's entry and whether
// one was removed. It does not modify refs.
func withoutMeetingSource(refs []model.DictSourceRef, meetingID string) ([]model.DictSourceRef, bool) {
	out := make([]model.DictSourceRef, 0, len(refs))
	removed := false
	for _, ref := range refs {
		if ref.Kind == model.DictSourceMeeting && ref.Ref == meetingID {
			removed = true
			continue
		}
		out = append(out, ref)
	}
	return out, removed
}

// dropTermsAccount removes accountID from the run state's accountIds while
// accountRecords still equals records and the meeting is not linked to
// accountID. dropped is false when either changed (the entry stays for the
// next sweep); other failures are returned.
func (r *DynamoDBRepository) dropTermsAccount(ctx context.Context, ownerID, meetingID, accountID string, records int64) (dropped bool, err error) {
	recordsCond := expression.Name("accountRecords").Equal(expression.Value(records))
	if records == 0 {
		recordsCond = expression.AttributeNotExists(expression.Name("accountRecords")).Or(recordsCond)
	}
	condition := expression.Name("ownerId").Equal(expression.Value(ownerID)).
		And(expression.Contains(expression.Name("accountIds"), accountID)).
		And(recordsCond)
	update := expression.Delete(expression.Name("accountIds"), expression.Value(&types.AttributeValueMemberSS{Value: []string{accountID}}))
	check, err := r.meetingConditionCheck(ownerID, meetingID, expression.AttributeNotExists(expression.Name("accountId")).
		Or(expression.Name("accountId").NotEqual(expression.Value(accountID))))
	if err != nil {
		return false, err
	}
	err = r.termsWriteWithCheck(ctx, meetingID, check, update, condition)
	if errors.Is(err, ErrConditionFailed) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("drop swept terms account: %w", err)
	}
	return true, nil
}
