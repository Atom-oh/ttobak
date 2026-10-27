package repository

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

// Dictionary v2 condition outcomes; each also matches ErrConditionFailed.
// ErrDictTermConflict: the row exists (create) or a version/state/origin/
// userEditedAt guard failed. ErrDictObservationExists: meeting already counted.
// ErrLegacyBlobChanged: the legacy DICTIONARY item was migrated or no longer
// has the observed blobRevision/updatedAt, so a migration write was refused.
var (
	ErrDictTermConflict      = errors.New("dictionary term guard failed")
	ErrDictObservationExists = errors.New("dictionary observation already recorded")
	ErrLegacyBlobChanged     = errors.New("legacy dictionary changed or was migrated")
	// ErrDictSourceMeetingGone: the observation's source meeting no longer
	// exists, so nothing was written (deletion cleanup already ran or will).
	ErrDictSourceMeetingGone = errors.New("dictionary observation source meeting is gone")
)

// ErrNotAccountMember: the writer has no MEMBER# row on the account, so an
// account-scope term write was refused. ErrInvalidInput: an account-scope
// write carried no, or a mismatching, MembershipCheck. Neither matches
// ErrConditionFailed: they are authorization outcomes, not version races.
var (
	ErrNotAccountMember = errors.New("not a member of the account")
	ErrInvalidInput     = errors.New("invalid dictionary write input")
)

// MembershipCheck names the member whose ACCOUNT#{id}/MEMBER#{userId} row must
// exist in the same transaction as an account-scope term write.
type MembershipCheck struct {
	AccountID, UserID string
}

// memberCheck builds the membership ConditionCheck for an account partition,
// or nil for personal and common partitions (member is ignored there). An
// account partition with a nil or mismatching member is ErrInvalidInput.
func (r *DictionaryRepository) memberCheck(pk string, member *MembershipCheck) (*types.TransactWriteItem, error) {
	scope, accountID, _ := model.TermScopeOf(pk)
	if strings.HasPrefix(pk, model.PrefixAccount) && scope != model.TermScopeAccount {
		return nil, fmt.Errorf("%w: malformed account partition", ErrInvalidInput)
	}
	if scope != model.TermScopeAccount {
		return nil, nil
	}
	if member == nil || member.UserID == "" || member.AccountID != accountID {
		return nil, fmt.Errorf("%w: account term write needs a matching membership check", ErrInvalidInput)
	}
	expr, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return nil, err
	}
	return &types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
		TableName: aws.String(r.tableName), Key: dictKey(model.PrefixAccount+accountID, model.PrefixMember+member.UserID),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
	}}, nil
}

// withMemberCheck appends the membership check last (so existing per-item
// error kinds keep their indexes) and returns its index, or -1 for none.
func (r *DictionaryRepository) withMemberCheck(items []types.TransactWriteItem, pk string, member *MembershipCheck) ([]types.TransactWriteItem, int, error) {
	check, err := r.memberCheck(pk, member)
	if err != nil || check == nil {
		return items, -1, err
	}
	return append(items, *check), len(items), nil
}

// dictMemberTransactError maps a failed membership check at memberIdx to
// ErrNotAccountMember ahead of the write's own guards (kinds, by item index).
func dictMemberTransactError(err error, memberIdx int, kinds []error) error {
	var cancelled *types.TransactionCanceledException
	if memberIdx >= 0 && errors.As(err, &cancelled) && memberIdx < len(cancelled.CancellationReasons) &&
		aws.ToString(cancelled.CancellationReasons[memberIdx].Code) == "ConditionalCheckFailed" {
		return ErrNotAccountMember
	}
	return dictTransactError(err, kinds)
}

// Cancelled dictionary transactions with no failed condition. Neither matches
// ErrConditionFailed. A cancelled transaction applied none of its items.
// ErrDictTransactionConflict (which also matches ErrDictTransactionCancelled)
// means a concurrent write touched an item; re-reading and retrying is safe.
var (
	ErrDictTransactionCancelled = errors.New("dictionary transaction cancelled")
	ErrDictTransactionConflict  = errors.New("dictionary transaction conflicted with a concurrent write")
)

// dictTransactItemLimit is the number of term puts per migration transaction;
// with the legacy blob ConditionCheck it fills DynamoDB's 100-action limit.
const dictTransactItemLimit = 99

// LegacyBlobGuard pins a migration write to the legacy blob it copied: the
// DICTIONARY item must still lack migratedAt and have the observed
// blobRevision and updatedAt (absent when zero/empty, for pre-v2 blobs).
type LegacyBlobGuard struct {
	UserID    string
	Revision  int64
	UpdatedAt string
}

// condition is shared by MarkDictionaryMigrated and every migration
// transaction's ConditionCheck.
func (g LegacyBlobGuard) condition() expression.ConditionBuilder {
	revision := expression.AttributeNotExists(expression.Name("blobRevision"))
	if g.Revision != 0 {
		revision = expression.Name("blobRevision").Equal(expression.Value(g.Revision))
	}
	updatedAt := expression.AttributeNotExists(expression.Name("updatedAt"))
	if g.UpdatedAt != "" {
		updatedAt = expression.Name("updatedAt").Equal(expression.Value(g.UpdatedAt))
	}
	return expression.AttributeExists(expression.Name("PK")).
		And(expression.AttributeNotExists(expression.Name("migratedAt"))).
		And(revision).And(updatedAt)
}

// legacyBlobCheck is the ConditionCheck that pins a transaction to the blob.
func (r *DictionaryRepository) legacyBlobCheck(g LegacyBlobGuard) (types.TransactWriteItem, error) {
	if g.UserID == "" {
		return types.TransactWriteItem{}, fmt.Errorf("legacy blob guard requires a user")
	}
	expr, err := expression.NewBuilder().WithCondition(g.condition()).Build()
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	return types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
		TableName: aws.String(r.tableName), Key: dictKey(model.PrefixUser+g.UserID, model.PrefixDictionary),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}}, nil
}

// guardedLegacyWrite runs one migration write with the blob ConditionCheck
// appended; a failed check maps to ErrLegacyBlobChanged ahead of the write's
// own guard, which maps to ErrDictTermConflict.
func (r *DictionaryRepository) guardedLegacyWrite(ctx context.Context, guard LegacyBlobGuard, write types.TransactWriteItem) error {
	check, err := r.legacyBlobCheck(guard)
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{write, check}})
	if err == nil {
		return nil
	}
	var cancelled *types.TransactionCanceledException
	if errors.As(err, &cancelled) && len(cancelled.CancellationReasons) > 1 &&
		aws.ToString(cancelled.CancellationReasons[1].Code) == "ConditionalCheckFailed" {
		return dictConditionError(ErrLegacyBlobChanged)
	}
	return dictTransactError(err, []error{ErrDictTermConflict})
}

// DictionaryRepository provides DynamoDB operations for user dictionaries
type DictionaryRepository struct {
	client    *dynamodb.Client
	tableName string
}

// NewDictionaryRepository creates a new dictionary repository
func NewDictionaryRepository(client *dynamodb.Client, tableName string) *DictionaryRepository {
	return &DictionaryRepository{
		client:    client,
		tableName: tableName,
	}
}

// DictMergeInput is one observation merged into an existing auto term.
type DictMergeInput struct {
	PK              string
	TermKey         string
	Observation     *model.DictObservation
	HeardAs         []string              // deduped, trimmed union computed by the caller
	SourceTypes     []string              // union computed by the caller; nil leaves it as is
	Sources         []model.DictSourceRef // new evidence list computed by the caller; nil leaves it as is
	SeenAt          string
	ExpectedVersion int64
	// SkipHit records the observation without ADD hits: the meeting was
	// already counted and only its observation row was pruned.
	SkipHit bool
	// Member is required when PK is an ACCOUNT# partition and ignored otherwise.
	Member *MembershipCheck
}

// DictPromotion is the untainted run's content that replaces a suggestion's
// when it is promoted. Hits restarts at 1: the tainted run that created the
// suggestion is never counted toward the active term.
type DictPromotion struct {
	DisplayAs            string
	Phrase               string
	Type                 string
	Confidence           float64
	TranscribeKo         bool
	TranscribeEn         bool
	FirstSourceMeetingID string
}

// DictTermChange lists the user-editable fields; nil leaves a field as is.
// displayAs is the key's source, so renaming is a new term, not an edit.
type DictTermChange struct {
	DisplayAs    *string // same termKey only (e.g. a casing change); the caller checks
	Phrase       *string
	HeardAs      *[]string
	AliasMode    *string
	Type         *string
	State        *string
	TranscribeKo *bool
	TranscribeEn *bool
	// LegacyDisplayEmpty keeps the legacy response's empty displayAs.
	LegacyDisplayEmpty *bool
}

// VocabularyStateChange lists VOCAB# fields updated under a version guard.
type VocabularyStateChange struct {
	ActiveName  *string
	Status      *string
	ActiveReady *bool
	FailureCode *string
	TermCount   *int
	// A/B builder fields (service/vocabulary_build.go).
	InactiveName        *string
	RetiredName         *string
	ActiveFingerprint   *string
	BuildingFingerprint *string
	BuildStartedAt      *string
	BuildSubmitted      *bool
	BypassPending       *bool
	FlippedAt           *string
	EmptySince          *string
	SubmittedAt         *string
	// ExpectBuildingFingerprint adds a buildingFingerprint condition to
	// UpdateVocabularyState (submit claims and flips).
	ExpectBuildingFingerprint *string
}

// DictWriteResult surfaces a failed best-effort observation cleanup after a
// committed term write.
type DictWriteResult struct {
	CleanupErr error
}

func dictKey(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"PK": &types.AttributeValueMemberS{Value: pk},
		"SK": &types.AttributeValueMemberS{Value: sk},
	}
}

// setPresent adds SET clauses for the non-nil pointer values.
func setPresent(update expression.UpdateBuilder, fields map[string]interface{}) expression.UpdateBuilder {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic expressions
	for _, name := range names {
		v := reflect.ValueOf(fields[name])
		if v.Kind() == reflect.Pointer && !v.IsNil() {
			update = update.Set(expression.Name(name), expression.Value(v.Elem().Interface()))
		}
	}
	return update
}

func dictConditionError(kind error) error {
	return fmt.Errorf("%w: %w", ErrConditionFailed, kind)
}

// dictTransactError maps the first failed item's condition to kinds[i].
func dictTransactError(err error, kinds []error) error {
	var cancelled *types.TransactionCanceledException
	if errors.As(err, &cancelled) {
		for i, reason := range cancelled.CancellationReasons {
			if aws.ToString(reason.Code) == "ConditionalCheckFailed" && i < len(kinds) {
				return dictConditionError(kinds[i])
			}
		}
		for _, reason := range cancelled.CancellationReasons {
			if aws.ToString(reason.Code) == "TransactionConflict" {
				return fmt.Errorf("%w: %w: %w", ErrDictTransactionConflict, ErrDictTransactionCancelled, err)
			}
		}
		return fmt.Errorf("%w, retry: %w", ErrDictTransactionCancelled, err)
	}
	return err
}

func dictSingleError(err error, kind error) error {
	var failed *types.ConditionalCheckFailedException
	if errors.As(err, &failed) {
		return dictConditionError(kind)
	}
	return err
}

func (r *DictionaryRepository) putIfAbsent(item interface{}) (types.TransactWriteItem, error) {
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	expr, err := expression.NewBuilder().WithCondition(expression.AttributeNotExists(expression.Name("PK"))).Build()
	if err != nil {
		return types.TransactWriteItem{}, err
	}
	return types.TransactWriteItem{Put: &types.Put{
		TableName: aws.String(r.tableName), Item: av,
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
	}}, nil
}

// GetDictionary retrieves a user's dictionary from DynamoDB
// Returns nil if the user has no dictionary yet
func (r *DictionaryRepository) GetDictionary(ctx context.Context, userID string) (*model.UserDictionary, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.tableName),
		Key:            dictKey(model.PrefixUser+userID, model.PrefixDictionary),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get dictionary: %w", err)
	}

	if result.Item == nil {
		return nil, nil
	}

	var dict model.UserDictionary
	if err := attributevalue.UnmarshalMap(result.Item, &dict); err != nil {
		return nil, fmt.Errorf("failed to unmarshal dictionary: %w", err)
	}

	return &dict, nil
}

// SaveDictionary writes the legacy blob's terms and vocabulary fields and
// bumps blobRevision. It is a field-scoped upsert so it never clears migratedAt.
func (r *DictionaryRepository) SaveDictionary(ctx context.Context, dict *model.UserDictionary) error {
	update := expression.Set(expression.Name("userId"), expression.Value(dict.UserID)).
		Set(expression.Name("terms"), expression.Value(dict.Terms)).
		Set(expression.Name("vocabularyName"), expression.Value(dict.VocabularyName)).
		Set(expression.Name("vocabularyStatus"), expression.Value(dict.VocabularyStatus)).
		Set(expression.Name("updatedAt"), expression.Value(dict.UpdatedAt)).
		Set(expression.Name("entityType"), expression.Value("DICTIONARY")).
		Add(expression.Name("blobRevision"), expression.Value(1))
	expr, err := expression.NewBuilder().WithUpdate(update).Build()
	if err != nil {
		return fmt.Errorf("failed to build dictionary update: %w", err)
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(r.tableName),
		Key:                       dictKey(model.PrefixUser+dict.UserID, model.PrefixDictionary),
		UpdateExpression:          expr.Update(),
		ExpressionAttributeNames:  expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return fmt.Errorf("failed to save dictionary: %w", err)
	}
	return nil
}

// UpdateLegacyVocabularyStatus moves the blob's vocabularyStatus from one
// observed value to another for the observed vocabulary name only.
func (r *DictionaryRepository) UpdateLegacyVocabularyStatus(ctx context.Context, userID, vocabName, from, to string) error {
	condition := expression.AttributeExists(expression.Name("PK")).
		And(expression.Name("vocabularyName").Equal(expression.Value(vocabName))).
		And(expression.Name("vocabularyStatus").Equal(expression.Value(from)))
	return r.guardedUpdate(ctx, dictKey(model.PrefixUser+userID, model.PrefixDictionary),
		expression.Set(expression.Name("vocabularyStatus"), expression.Value(to)), condition)
}

// MarkDictionaryMigrated sets migratedAt once, only while the blob is still
// at the revision and updatedAt that migration copied. It never removes blob
// terms.
func (r *DictionaryRepository) MarkDictionaryMigrated(ctx context.Context, userID, now string, observedRevision int64, observedUpdatedAt string) error {
	guard := LegacyBlobGuard{UserID: userID, Revision: observedRevision, UpdatedAt: observedUpdatedAt}
	return r.guardedUpdate(ctx, dictKey(model.PrefixUser+userID, model.PrefixDictionary),
		expression.Set(expression.Name("migratedAt"), expression.Value(now)), guard.condition())
}

// ReplaceLegacyMigratedTerm rewrites (next != nil) or deletes (next == nil) a
// pristine legacy-migrated row: active, manual, unedited, at expectedVersion,
// in one transaction with the legacy blob ConditionCheck. Migration uses it to
// bring earlier copies back in line with the observed blob.
func (r *DictionaryRepository) ReplaceLegacyMigratedTerm(ctx context.Context, guard LegacyBlobGuard, pk, termKey string, expectedVersion int64, next *model.DictTerm) error {
	if strings.HasPrefix(pk, model.PrefixAccount) || (next != nil && strings.HasPrefix(next.PK, model.PrefixAccount)) {
		return fmt.Errorf("%w: legacy migration is personal-only", ErrInvalidInput)
	}
	if pk != model.PrefixUser+guard.UserID {
		return fmt.Errorf("invalid legacy term replacement")
	}
	condition := expression.Name("legacyMigrated").Equal(expression.Value(true)).
		And(expression.Name("version").Equal(expression.Value(expectedVersion))).
		And(expression.Name("state").Equal(expression.Value(model.TermStateActive))).
		And(expression.Name("origin").Equal(expression.Value(model.TermOriginManual))).
		And(expression.AttributeNotExists(expression.Name("userEditedAt")))
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return err
	}
	if next == nil {
		return r.guardedLegacyWrite(ctx, guard, types.TransactWriteItem{Delete: &types.Delete{
			TableName: aws.String(r.tableName), Key: dictKey(pk, model.DictTermSK(termKey)),
			ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
			ExpressionAttributeValues: expr.Values(),
		}})
	}
	if next.PK != pk || next.TermKey != termKey || next.SK != model.DictTermSK(termKey) || !next.LegacyMigrated {
		return fmt.Errorf("invalid legacy term replacement")
	}
	replacement := *next
	replacement.Version = expectedVersion + 1
	av, err := attributevalue.MarshalMap(&replacement)
	if err != nil {
		return err
	}
	return r.guardedLegacyWrite(ctx, guard, types.TransactWriteItem{Put: &types.Put{
		TableName: aws.String(r.tableName), Item: av,
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}})
}

func (r *DictionaryRepository) queryPrefix(ctx context.Context, pk, prefix string, visit func(map[string]types.AttributeValue) error) error {
	keyEx := expression.Key("PK").Equal(expression.Value(pk)).And(expression.Key("SK").BeginsWith(prefix))
	expr, err := expression.NewBuilder().WithKeyCondition(keyEx).Build()
	if err != nil {
		return err
	}
	pages := dynamodb.NewQueryPaginator(r.client, &dynamodb.QueryInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		KeyConditionExpression: expr.KeyCondition(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, item := range page.Items {
			if err := visit(item); err != nil {
				return err
			}
		}
	}
	return nil
}

// ListDictTerms returns every term row, including tombstones, in a partition.
func (r *DictionaryRepository) ListDictTerms(ctx context.Context, pk string) ([]model.DictTerm, error) {
	var terms []model.DictTerm
	err := r.queryPrefix(ctx, pk, model.PrefixDictTerm, func(item map[string]types.AttributeValue) error {
		var term model.DictTerm
		if err := attributevalue.UnmarshalMap(item, &term); err != nil {
			return err
		}
		terms = append(terms, term)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list dictionary terms: %w", err)
	}
	return terms, nil
}

// ListDictObs returns observation rows in a partition; a non-empty termKey
// limits them to that term.
func (r *DictionaryRepository) ListDictObs(ctx context.Context, pk, termKey string) ([]model.DictObservation, error) {
	prefix := model.PrefixDictObs
	if termKey != "" {
		prefix += termKey + "#"
	}
	var rows []model.DictObservation
	err := r.queryPrefix(ctx, pk, prefix, func(item map[string]types.AttributeValue) error {
		var obs model.DictObservation
		if err := attributevalue.UnmarshalMap(item, &obs); err != nil {
			return err
		}
		rows = append(rows, obs)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list dictionary observations: %w", err)
	}
	return rows, nil
}

// GetDictTerm reads one term row consistently; nil means absent.
func (r *DictionaryRepository) GetDictTerm(ctx context.Context, pk, termKey string) (*model.DictTerm, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), Key: dictKey(pk, model.DictTermSK(termKey)), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get dictionary term: %w", err)
	}
	if len(result.Item) == 0 {
		return nil, nil
	}
	var term model.DictTerm
	if err := attributevalue.UnmarshalMap(result.Item, &term); err != nil {
		return nil, fmt.Errorf("failed to decode dictionary term: %w", err)
	}
	return &term, nil
}

// PutDictTermsIfAbsent creates missing migration rows in transactions of at
// most 99 puts plus the legacy blob ConditionCheck; existing rows, including
// tombstones, are left untouched. A failed check stops with
// ErrLegacyBlobChanged, so a run that copied a superseded blob cannot create
// rows after another run migrated the current one.
func (r *DictionaryRepository) PutDictTermsIfAbsent(ctx context.Context, guard LegacyBlobGuard, terms []model.DictTerm) (int, error) {
	check, err := r.legacyBlobCheck(guard)
	if err != nil {
		return 0, err
	}
	for i := range terms {
		if strings.HasPrefix(terms[i].PK, model.PrefixAccount) {
			return 0, fmt.Errorf("%w: legacy migration is personal-only", ErrInvalidInput)
		}
		if terms[i].PK != model.PrefixUser+guard.UserID {
			return 0, fmt.Errorf("invalid legacy term create")
		}
	}
	created := 0
	for start := 0; start < len(terms); start += dictTransactItemLimit {
		end := start + dictTransactItemLimit
		if end > len(terms) {
			end = len(terms)
		}
		n, err := r.putChunkIfAbsent(ctx, check, append([]model.DictTerm(nil), terms[start:end]...))
		created += n
		if err != nil {
			return created, err
		}
	}
	return created, nil
}

func (r *DictionaryRepository) putChunkIfAbsent(ctx context.Context, check types.TransactWriteItem, pending []model.DictTerm) (int, error) {
	var lastErr error
	for attempt := 0; attempt < 4 && len(pending) > 0; attempt++ {
		items := make([]types.TransactWriteItem, len(pending), len(pending)+1)
		for i := range pending {
			item, err := r.putIfAbsent(&pending[i])
			if err != nil {
				return 0, err
			}
			items[i] = item
		}
		items = append(items, check) // last: index len(pending)
		_, lastErr = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
		var cancelled *types.TransactionCanceledException
		if lastErr == nil {
			return len(pending), nil
		} else if !errors.As(lastErr, &cancelled) {
			return 0, fmt.Errorf("failed to create dictionary terms: %w", lastErr)
		}
		if len(cancelled.CancellationReasons) > len(pending) &&
			aws.ToString(cancelled.CancellationReasons[len(pending)].Code) == "ConditionalCheckFailed" {
			return 0, dictConditionError(ErrLegacyBlobChanged)
		}
		var remaining []model.DictTerm
		for i, item := range pending {
			if i >= len(cancelled.CancellationReasons) || aws.ToString(cancelled.CancellationReasons[i].Code) != "ConditionalCheckFailed" {
				remaining = append(remaining, item)
			}
		}
		pending = remaining
	}
	if len(pending) == 0 {
		return 0, nil
	}
	return 0, fmt.Errorf("failed to create dictionary terms after retries: %w", lastErr)
}

// CreateAutoTerm creates a new term and its first observation atomically. An
// existing row, including a tombstone, cancels it with ErrDictTermConflict.
// Account-scope callers must use CreateAutoTermChecked.
func (r *DictionaryRepository) CreateAutoTerm(ctx context.Context, term *model.DictTerm, obs *model.DictObservation) error {
	return r.CreateAutoTermChecked(ctx, term, obs, nil)
}

// CreateAutoTermChecked is CreateAutoTerm with the membership ConditionCheck
// an ACCOUNT# partition requires; member is ignored for other partitions.
func (r *DictionaryRepository) CreateAutoTermChecked(ctx context.Context, term *model.DictTerm, obs *model.DictObservation, member *MembershipCheck) error {
	if term == nil || obs == nil || term.PK != obs.PK || obs.TermKey != term.TermKey {
		return fmt.Errorf("invalid dictionary term create")
	}
	termPut, err := r.putIfAbsent(term)
	if err != nil {
		return err
	}
	obsPut, err := r.putIfAbsent(obs)
	if err != nil {
		return err
	}
	items, err := r.withSourceMeetingCheck([]types.TransactWriteItem{termPut, obsPut}, obs)
	if err != nil {
		return err
	}
	items, memberIdx, err := r.withMemberCheck(items, term.PK, member)
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err != nil {
		return dictMemberTransactError(err, memberIdx, []error{ErrDictTermConflict, ErrDictObservationExists, ErrDictSourceMeetingGone})
	}
	return nil
}

// withSourceMeetingCheck appends a ConditionCheck that the observation's
// source meeting (USER#{owner}/MEETING#{meeting}) still exists, so a worker
// that outlives a meeting deletion cannot leave observations behind (A8).
func (r *DictionaryRepository) withSourceMeetingCheck(items []types.TransactWriteItem, obs *model.DictObservation) ([]types.TransactWriteItem, error) {
	if obs.OwnerID == "" || obs.MeetingID == "" {
		return items, nil
	}
	expr, err := expression.NewBuilder().WithCondition(expression.AttributeExists(expression.Name("PK"))).Build()
	if err != nil {
		return nil, err
	}
	return append(items, types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
		TableName: aws.String(r.tableName), Key: dictKey(model.PrefixUser+obs.OwnerID, model.PrefixMeeting+obs.MeetingID),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
	}}), nil
}

// MergeAutoObservation records a new observation and bumps the auto term in
// one transaction. hits increases only when the observation row is new and
// SkipHit is false.
func (r *DictionaryRepository) MergeAutoObservation(ctx context.Context, in DictMergeInput) error {
	return r.observeAutoTerm(ctx, in, model.TermStateActive, nil)
}

// PromoteSuggestedTerm turns an unedited suggested auto term active at the
// expected version and records the observation in the same transaction (A4:
// an untainted run confirms a suggestion). The suggestion's content came from
// a tainted run, so heardAs, sourceTypes and the promotion fields are replaced
// by the untainted run's values and hits is set to 1, not incremented.
func (r *DictionaryRepository) PromoteSuggestedTerm(ctx context.Context, in DictMergeInput, content DictPromotion) error {
	if strings.TrimSpace(content.DisplayAs) == "" || len(in.SourceTypes) == 0 {
		return fmt.Errorf("invalid dictionary term promotion")
	}
	return r.observeAutoTerm(ctx, in, model.TermStateSuggested, &content)
}

// observeAutoTerm is the shared obs Put + guarded term Update. fromState is
// the state the term must be in; promotion, when set, makes it active.
func (r *DictionaryRepository) observeAutoTerm(ctx context.Context, in DictMergeInput, fromState string, promotion *DictPromotion) error {
	if in.Observation == nil || in.Observation.PK != in.PK || in.Observation.TermKey != in.TermKey ||
		in.Observation.SK != model.DictObsSK(in.TermKey, in.Observation.MeetingID) {
		return fmt.Errorf("invalid dictionary observation merge")
	}
	obsPut, err := r.putIfAbsent(in.Observation)
	if err != nil {
		return err
	}
	condition := expression.Name("version").Equal(expression.Value(in.ExpectedVersion)).
		And(expression.Name("state").Equal(expression.Value(fromState))).
		And(expression.Name("origin").Equal(expression.Value(model.TermOriginAuto))).
		And(expression.AttributeNotExists(expression.Name("userEditedAt")))
	update := expression.Set(expression.Name("heardAs"), expression.Value(in.HeardAs)).
		Set(expression.Name("lastSeenAt"), expression.Value(in.SeenAt)).
		Set(expression.Name("updatedAt"), expression.Value(in.SeenAt)).
		Set(expression.Name("version"), expression.Name("version").Plus(expression.Value(1)))
	switch {
	case promotion != nil:
		update = update.Set(expression.Name("state"), expression.Value(model.TermStateActive)).
			Set(expression.Name("displayAs"), expression.Value(strings.TrimSpace(promotion.DisplayAs))).
			Set(expression.Name("confidence"), expression.Value(promotion.Confidence)).
			Set(expression.Name("transcribeKo"), expression.Value(promotion.TranscribeKo)).
			Set(expression.Name("transcribeEn"), expression.Value(promotion.TranscribeEn)).
			Set(expression.Name("firstSourceMeetingId"), expression.Value(promotion.FirstSourceMeetingID)).
			Set(expression.Name("hits"), expression.Value(1))
		for _, field := range [][2]string{{"phrase", promotion.Phrase}, {"type", promotion.Type}} {
			if field[1] != "" {
				update = update.Set(expression.Name(field[0]), expression.Value(field[1]))
			} else {
				update = update.Remove(expression.Name(field[0]))
			}
		}
	case !in.SkipHit:
		update = update.Add(expression.Name("hits"), expression.Value(1))
	}
	if len(in.SourceTypes) > 0 {
		update = update.Set(expression.Name("sourceTypes"), expression.Value(in.SourceTypes))
	}
	if len(in.Sources) > 0 {
		update = update.Set(expression.Name("sources"), expression.Value(in.Sources))
	}
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return err
	}
	items, err := r.withSourceMeetingCheck([]types.TransactWriteItem{obsPut, {Update: &types.Update{
		TableName: aws.String(r.tableName), Key: dictKey(in.PK, model.DictTermSK(in.TermKey)),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}}}, in.Observation)
	if err != nil {
		return err
	}
	items, memberIdx, err := r.withMemberCheck(items, in.PK, in.Member)
	if err != nil {
		return err
	}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err != nil {
		return dictMemberTransactError(err, memberIdx, []error{ErrDictObservationExists, ErrDictTermConflict, ErrDictSourceMeetingGone})
	}
	return nil
}

// guardedUpdate runs one conditional UpdateItem.
func (r *DictionaryRepository) guardedUpdate(ctx context.Context, key map[string]types.AttributeValue, update expression.UpdateBuilder, condition expression.ConditionBuilder) error {
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return err
	}
	_, err = r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(r.tableName), Key: key,
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	})
	return dictSingleError(err, ErrDictTermConflict)
}

// updateTerm runs a guarded term update; an ACCOUNT# partition runs it as a
// transaction with the membership ConditionCheck.
func (r *DictionaryRepository) updateTerm(ctx context.Context, pk, termKey string, update expression.UpdateBuilder, condition expression.ConditionBuilder, member *MembershipCheck) error {
	check, err := r.memberCheck(pk, member)
	if err != nil {
		return err
	}
	if check != nil {
		expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
		if err != nil {
			return err
		}
		items := []types.TransactWriteItem{{Update: &types.Update{
			TableName: aws.String(r.tableName), Key: dictKey(pk, model.DictTermSK(termKey)),
			UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
			ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
		}}, *check}
		_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
		if err != nil {
			return dictMemberTransactError(err, 1, []error{ErrDictTermConflict})
		}
		return nil
	}
	return r.guardedUpdate(ctx, dictKey(pk, model.DictTermSK(termKey)), update, condition)
}

// UpdateDictTerm applies a user edit under a version guard and sets
// userEditedAt, which blocks every later automatic write to the term.
func (r *DictionaryRepository) UpdateDictTerm(ctx context.Context, pk, termKey string, change DictTermChange, actor, now string, expectedVersion int64, member *MembershipCheck) error {
	if change.State != nil && *change.State == model.TermStateDeleted {
		return fmt.Errorf("use TombstoneDictTerm to delete a dictionary term")
	}
	update := expression.Set(expression.Name("userEditedAt"), expression.Value(now)).
		Set(expression.Name("updatedBy"), expression.Value(actor)).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name("version"), expression.Name("version").Plus(expression.Value(1)))
	update = setPresent(update, map[string]interface{}{
		"displayAs": change.DisplayAs, "legacyDisplayEmpty": change.LegacyDisplayEmpty,
		"phrase": change.Phrase, "aliasMode": change.AliasMode,
		"type": change.Type, "state": change.State, "heardAs": change.HeardAs,
		"transcribeKo": change.TranscribeKo, "transcribeEn": change.TranscribeEn,
	})
	condition := expression.Name("version").Equal(expression.Value(expectedVersion)).
		And(expression.Name("state").NotEqual(expression.Value(model.TermStateDeleted)))
	return r.updateTerm(ctx, pk, termKey, update, condition, member)
}

// TombstoneDictTerm replaces a term with a tombstone so it is never
// automatically re-added, then deletes its observations best effort.
// The tombstone keeps termKey, scope, origin, state, version and timestamps;
// content, counters and recognition flags are cleared.
func (r *DictionaryRepository) TombstoneDictTerm(ctx context.Context, pk, termKey, actor, now string, expectedVersion int64, member *MembershipCheck) (DictWriteResult, error) {
	update := expression.Set(expression.Name("state"), expression.Value(model.TermStateDeleted)).
		Set(expression.Name("deletedAt"), expression.Value(now)).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name("updatedBy"), expression.Value(actor)).
		Set(expression.Name("version"), expression.Name("version").Plus(expression.Value(1))).
		Remove(expression.Name("displayAs")).
		Remove(expression.Name("phrase")).
		Remove(expression.Name("heardAs")).
		Remove(expression.Name("sourceTypes")).
		Remove(expression.Name("firstSourceMeetingId")).
		Remove(expression.Name("confidence")).
		Remove(expression.Name("type")).
		Remove(expression.Name("aliasMode")).
		Remove(expression.Name("lastSeenAt")).
		Remove(expression.Name("legacyDisplayEmpty")).
		Set(expression.Name("hits"), expression.Value(0)).
		Set(expression.Name("transcribeKo"), expression.Value(false)).
		Set(expression.Name("transcribeEn"), expression.Value(false))
	condition := expression.Name("version").Equal(expression.Value(expectedVersion)).
		And(expression.Name("state").NotEqual(expression.Value(model.TermStateDeleted)))
	if err := r.updateTerm(ctx, pk, termKey, update, condition, member); err != nil {
		return DictWriteResult{}, err
	}
	return DictWriteResult{CleanupErr: r.deleteTermObservations(ctx, pk, termKey)}, nil
}

// RestoreDictTerm replaces a tombstone with the supplied term content. The
// stored version becomes expectedVersion+1.
func (r *DictionaryRepository) RestoreDictTerm(ctx context.Context, term *model.DictTerm, expectedVersion int64, member *MembershipCheck) error {
	if term == nil || term.State == model.TermStateDeleted {
		return fmt.Errorf("invalid dictionary term restore")
	}
	restored := *term
	restored.Version = expectedVersion + 1
	restored.DeletedAt = ""
	av, err := attributevalue.MarshalMap(&restored)
	if err != nil {
		return err
	}
	condition := expression.Name("state").Equal(expression.Value(model.TermStateDeleted)).
		And(expression.Name("version").Equal(expression.Value(expectedVersion)))
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return err
	}
	put := types.TransactWriteItem{Put: &types.Put{
		TableName: aws.String(r.tableName), Item: av,
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}}
	items, memberIdx, err := r.withMemberCheck([]types.TransactWriteItem{put}, term.PK, member)
	if err != nil {
		return err
	}
	if memberIdx >= 0 {
		return r.transactChecked(ctx, items, memberIdx)
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: put.Put.TableName, Item: put.Put.Item,
		ConditionExpression: put.Put.ConditionExpression, ExpressionAttributeNames: put.Put.ExpressionAttributeNames,
		ExpressionAttributeValues: put.Put.ExpressionAttributeValues,
	})
	return dictSingleError(err, ErrDictTermConflict)
}

// transactChecked runs a one-write transaction plus its membership check.
func (r *DictionaryRepository) transactChecked(ctx context.Context, items []types.TransactWriteItem, memberIdx int) error {
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		return dictMemberTransactError(err, memberIdx, []error{ErrDictTermConflict})
	}
	return nil
}

// CreateDictTerm creates one term row; any existing row, including a
// tombstone, fails with ErrDictTermConflict.
func (r *DictionaryRepository) CreateDictTerm(ctx context.Context, term *model.DictTerm, member *MembershipCheck) error {
	if term == nil || term.SK != model.DictTermSK(term.TermKey) || term.State == model.TermStateDeleted {
		return fmt.Errorf("invalid dictionary term create")
	}
	put, err := r.putIfAbsent(term)
	if err != nil {
		return err
	}
	items, memberIdx, err := r.withMemberCheck([]types.TransactWriteItem{put}, term.PK, member)
	if err != nil {
		return err
	}
	if memberIdx >= 0 {
		_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
		if err != nil {
			return dictMemberTransactError(err, memberIdx, []error{ErrDictTermConflict})
		}
		return nil
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: put.Put.TableName, Item: put.Put.Item,
		ConditionExpression: put.Put.ConditionExpression, ExpressionAttributeNames: put.Put.ExpressionAttributeNames,
	})
	return dictSingleError(err, ErrDictTermConflict)
}

// DeleteManualTerm hard-deletes an active manual term at the expected
// version. Auto, suggested and tombstoned rows never match, so the legacy
// whole-list PUT and legacy DELETE cannot remove or tombstone them.
func (r *DictionaryRepository) DeleteManualTerm(ctx context.Context, pk, termKey string, expectedVersion int64, member *MembershipCheck) error {
	return r.conditionalDelete(ctx, pk, termKey, member, expression.Name("version").Equal(expression.Value(expectedVersion)).
		And(expression.Name("origin").Equal(expression.Value(model.TermOriginManual))).
		And(expression.Name("state").Equal(expression.Value(model.TermStateActive))))
}

// DeleteTombstone hard-deletes a tombstone at the expected version; used to
// prune the oldest tombstones beyond the per-scope cap.
func (r *DictionaryRepository) DeleteTombstone(ctx context.Context, pk, termKey string, expectedVersion int64, member *MembershipCheck) error {
	return r.conditionalDelete(ctx, pk, termKey, member, expression.Name("version").Equal(expression.Value(expectedVersion)).
		And(expression.Name("state").Equal(expression.Value(model.TermStateDeleted))))
}

func (r *DictionaryRepository) conditionalDelete(ctx context.Context, pk, termKey string, member *MembershipCheck, condition expression.ConditionBuilder) error {
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return err
	}
	del := types.TransactWriteItem{Delete: &types.Delete{
		TableName: aws.String(r.tableName), Key: dictKey(pk, model.DictTermSK(termKey)),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}}
	items, memberIdx, err := r.withMemberCheck([]types.TransactWriteItem{del}, pk, member)
	if err != nil {
		return err
	}
	if memberIdx >= 0 {
		return r.transactChecked(ctx, items, memberIdx)
	}
	_, err = r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName), Key: dictKey(pk, model.DictTermSK(termKey)),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	return dictSingleError(err, ErrDictTermConflict)
}

// EvictAutoTerm deletes an unedited active or suggested auto term at the
// expected version, then deletes its observations best effort. Both states
// count toward the automatic-term cap.
func (r *DictionaryRepository) EvictAutoTerm(ctx context.Context, pk, termKey string, expectedVersion int64) (DictWriteResult, error) {
	condition := expression.Name("version").Equal(expression.Value(expectedVersion)).
		And(expression.Name("origin").Equal(expression.Value(model.TermOriginAuto))).
		And(expression.AttributeNotExists(expression.Name("userEditedAt"))).
		And(expression.Name("state").In(expression.Value(model.TermStateActive), expression.Value(model.TermStateSuggested)))
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return DictWriteResult{}, err
	}
	_, err = r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(r.tableName), Key: dictKey(pk, model.DictTermSK(termKey)),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	if err != nil {
		return DictWriteResult{}, dictSingleError(err, ErrDictTermConflict)
	}
	return DictWriteResult{CleanupErr: r.deleteTermObservations(ctx, pk, termKey)}, nil
}

// PutCommonDictTerm writes a whole Common term row and its audit row in one
// transaction. The term must be at expectedVersion (0: absent); the audit
// SK must be new. A tombstone (state deleted) then deletes the term's
// observations best effort. Only DICT#COMMON rows are accepted.
func (r *DictionaryRepository) PutCommonDictTerm(ctx context.Context, term *model.DictTerm, expectedVersion int64, entry *model.DictTermLog) (DictWriteResult, error) {
	if term == nil || entry == nil || term.PK != model.PrefixDictCommon || entry.PK != model.PrefixDictCommon ||
		term.SK != model.DictTermSK(term.TermKey) || entry.TermKey != term.TermKey ||
		!strings.HasPrefix(entry.SK, model.PrefixDictLog+term.TermKey+"#") {
		return DictWriteResult{}, fmt.Errorf("%w: common term write needs a matching common audit row", ErrInvalidInput)
	}
	av, err := attributevalue.MarshalMap(term)
	if err != nil {
		return DictWriteResult{}, err
	}
	condition := expression.AttributeNotExists(expression.Name("PK"))
	if expectedVersion != 0 {
		condition = expression.Name("version").Equal(expression.Value(expectedVersion))
	}
	expr, err := expression.NewBuilder().WithCondition(condition).Build()
	if err != nil {
		return DictWriteResult{}, err
	}
	logPut, err := r.putIfAbsent(entry)
	if err != nil {
		return DictWriteResult{}, err
	}
	items := []types.TransactWriteItem{{Put: &types.Put{
		TableName: aws.String(r.tableName), Item: av,
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	}}, logPut}
	if _, err := r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items}); err != nil {
		return DictWriteResult{}, dictTransactError(err, []error{ErrDictTermConflict, ErrDictTermConflict})
	}
	if term.State != model.TermStateDeleted {
		return DictWriteResult{}, nil
	}
	return DictWriteResult{CleanupErr: r.deleteTermObservations(ctx, term.PK, term.TermKey)}, nil
}

// ListDictTermLog returns a term's audit rows, newest first.
func (r *DictionaryRepository) ListDictTermLog(ctx context.Context, pk, termKey string) ([]model.DictTermLog, error) {
	var rows []model.DictTermLog
	err := r.queryPrefix(ctx, pk, model.PrefixDictLog+termKey+"#", func(item map[string]types.AttributeValue) error {
		var row model.DictTermLog
		if err := attributevalue.UnmarshalMap(item, &row); err != nil {
			return err
		}
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list dictionary term log: %w", err)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].SK > rows[j].SK })
	return rows, nil
}

// TrimDictTermLog deletes a term's audit rows beyond the newest keep.
func (r *DictionaryRepository) TrimDictTermLog(ctx context.Context, pk, termKey string, keep int) error {
	rows, err := r.ListDictTermLog(ctx, pk, termKey)
	if err != nil {
		return err
	}
	var errs []error
	for i := keep; i < len(rows); i++ {
		if _, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(r.tableName), Key: dictKey(pk, rows[i].SK)}); err != nil {
			errs = append(errs, fmt.Errorf("failed to delete dictionary term log: %w", err))
		}
	}
	return errors.Join(errs...)
}

// DeleteDictObs deletes one observation row.
func (r *DictionaryRepository) DeleteDictObs(ctx context.Context, pk, sk string) error {
	_, err := r.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(r.tableName), Key: dictKey(pk, sk)})
	if err != nil {
		return fmt.Errorf("failed to delete dictionary observation: %w", err)
	}
	return nil
}

func (r *DictionaryRepository) deleteTermObservations(ctx context.Context, pk, termKey string) error {
	rows, err := r.ListDictObs(ctx, pk, termKey)
	if err != nil {
		return err
	}
	var errs []error
	for _, row := range rows {
		if err := r.DeleteDictObs(ctx, pk, row.SK); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// GetVocabularyState reads one VOCAB# row consistently; nil means absent.
func (r *DictionaryRepository) GetVocabularyState(ctx context.Context, userID, vocabContext, lang string) (*model.VocabularyState, error) {
	result, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(r.tableName), ConsistentRead: aws.Bool(true),
		Key: dictKey(model.PrefixUser+userID, model.VocabSK(vocabContext, lang)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get vocabulary state: %w", err)
	}
	if len(result.Item) == 0 {
		return nil, nil
	}
	var state model.VocabularyState
	if err := attributevalue.UnmarshalMap(result.Item, &state); err != nil {
		return nil, fmt.Errorf("failed to decode vocabulary state: %w", err)
	}
	return &state, nil
}

// CreateLegacyVocabularyState writes the initial VOCAB# row once, pinned to
// the observed legacy blob. An existing row fails with ErrDictTermConflict.
func (r *DictionaryRepository) CreateLegacyVocabularyState(ctx context.Context, guard LegacyBlobGuard, state *model.VocabularyState) error {
	if state == nil || state.PK != model.PrefixUser+guard.UserID {
		return fmt.Errorf("invalid vocabulary state create")
	}
	put, err := r.putIfAbsent(state)
	if err != nil {
		return err
	}
	return r.guardedLegacyWrite(ctx, guard, put)
}

// CreateMigratedVocabularyState writes the initial VOCAB# row once for a v2
// write path, in one transaction with a ConditionCheck that the legacy
// DICTIONARY item is absent or already migrated; while a blob is still
// unmigrated only migration (CreateLegacyVocabularyState) may create the row.
// An existing row fails with ErrDictTermConflict, an unmigrated blob with
// ErrLegacyBlobChanged.
func (r *DictionaryRepository) CreateMigratedVocabularyState(ctx context.Context, userID string, state *model.VocabularyState) error {
	if userID == "" || state == nil || state.PK != model.PrefixUser+userID {
		return fmt.Errorf("invalid vocabulary state create")
	}
	put, err := r.putIfAbsent(state)
	if err != nil {
		return err
	}
	expr, err := expression.NewBuilder().WithCondition(
		expression.AttributeNotExists(expression.Name("PK")).
			Or(expression.AttributeExists(expression.Name("migratedAt")))).Build()
	if err != nil {
		return err
	}
	check := types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
		TableName: aws.String(r.tableName), Key: dictKey(model.PrefixUser+userID, model.PrefixDictionary),
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
	}}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{put, check}})
	return dictTransactError(err, []error{ErrDictTermConflict, ErrLegacyBlobChanged})
}

// ReconcileLegacyVocabularyState applies a migration change to an existing
// VOCAB# row at the expected version, pinned to the observed legacy blob.
func (r *DictionaryRepository) ReconcileLegacyVocabularyState(ctx context.Context, guard LegacyBlobGuard, vocabContext, lang string, expectedVersion int64, change VocabularyStateChange, now string) error {
	update := vocabularyStateUpdate(change, now)
	expr, err := expression.NewBuilder().WithUpdate(update).
		WithCondition(expression.Name("version").Equal(expression.Value(expectedVersion))).Build()
	if err != nil {
		return err
	}
	return r.guardedLegacyWrite(ctx, guard, types.TransactWriteItem{Update: &types.Update{
		TableName: aws.String(r.tableName), Key: dictKey(model.PrefixUser+guard.UserID, model.VocabSK(vocabContext, lang)),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}})
}

func vocabularyStateUpdate(change VocabularyStateChange, now string) expression.UpdateBuilder {
	update := expression.Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name("version"), expression.Name("version").Plus(expression.Value(1)))
	return setPresent(update, map[string]interface{}{
		"activeName": change.ActiveName, "status": change.Status, "failureCode": change.FailureCode,
		"activeReady": change.ActiveReady, "termCount": change.TermCount,
		"inactiveName": change.InactiveName, "retiredName": change.RetiredName,
		"activeFingerprint": change.ActiveFingerprint, "buildingFingerprint": change.BuildingFingerprint,
		"buildStartedAt": change.BuildStartedAt, "buildSubmitted": change.BuildSubmitted,
		"bypassPending": change.BypassPending, "flippedAt": change.FlippedAt, "emptySince": change.EmptySince,
		"submittedAt": change.SubmittedAt,
	})
}

// UpdateVocabularyState applies a change to a VOCAB# row at the expected
// version and bumps the version.
func (r *DictionaryRepository) UpdateVocabularyState(ctx context.Context, userID, vocabContext, lang string, expectedVersion int64, change VocabularyStateChange, now string) error {
	condition := expression.Name("version").Equal(expression.Value(expectedVersion))
	if change.ExpectBuildingFingerprint != nil {
		condition = condition.And(expression.Name("buildingFingerprint").Equal(expression.Value(*change.ExpectBuildingFingerprint)))
	}
	return r.guardedUpdate(ctx, dictKey(model.PrefixUser+userID, model.VocabSK(vocabContext, lang)), vocabularyStateUpdate(change, now), condition)
}
