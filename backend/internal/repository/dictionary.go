package repository

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

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
var (
	ErrDictTermConflict      = errors.New("dictionary term guard failed")
	ErrDictObservationExists = errors.New("dictionary observation already recorded")
)

// dictTransactItemLimit leaves headroom under DynamoDB's 100-item limit.
const dictTransactItemLimit = 99

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
	HeardAs         []string // deduped, trimmed union computed by the caller
	SeenAt          string
	ExpectedVersion int64
}

// DictTermChange lists the user-editable fields; nil leaves a field as is.
// displayAs is the key's source, so renaming is a new term, not an edit.
type DictTermChange struct {
	Phrase       *string
	HeardAs      *[]string
	AliasMode    *string
	Type         *string
	State        *string
	TranscribeKo *bool
	TranscribeEn *bool
}

// VocabularyStateChange lists VOCAB# fields updated under a version guard.
type VocabularyStateChange struct {
	ActiveName  *string
	Status      *string
	ActiveReady *bool
	FailureCode *string
	TermCount   *int
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
		return fmt.Errorf("dictionary transaction cancelled, retry: %w", err)
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

// SaveDictionary writes the legacy blob's terms and vocabulary fields. It is
// a field-scoped upsert so it never clears migratedAt.
func (r *DictionaryRepository) SaveDictionary(ctx context.Context, dict *model.UserDictionary) error {
	update := expression.Set(expression.Name("userId"), expression.Value(dict.UserID)).
		Set(expression.Name("terms"), expression.Value(dict.Terms)).
		Set(expression.Name("vocabularyName"), expression.Value(dict.VocabularyName)).
		Set(expression.Name("vocabularyStatus"), expression.Value(dict.VocabularyStatus)).
		Set(expression.Name("updatedAt"), expression.Value(dict.UpdatedAt)).
		Set(expression.Name("entityType"), expression.Value("DICTIONARY"))
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

// MarkDictionaryMigrated sets migratedAt once. It never removes blob terms.
func (r *DictionaryRepository) MarkDictionaryMigrated(ctx context.Context, userID, now string) error {
	condition := expression.AttributeExists(expression.Name("PK")).
		And(expression.AttributeNotExists(expression.Name("migratedAt")))
	return r.guardedUpdate(ctx, dictKey(model.PrefixUser+userID, model.PrefixDictionary),
		expression.Set(expression.Name("migratedAt"), expression.Value(now)), condition)
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

// PutDictTermsIfAbsent creates missing rows in transactions of at most 99;
// existing rows, including tombstones, are left untouched.
func (r *DictionaryRepository) PutDictTermsIfAbsent(ctx context.Context, terms []model.DictTerm) (int, error) {
	created := 0
	for start := 0; start < len(terms); start += dictTransactItemLimit {
		end := start + dictTransactItemLimit
		if end > len(terms) {
			end = len(terms)
		}
		n, err := r.putChunkIfAbsent(ctx, append([]model.DictTerm(nil), terms[start:end]...))
		created += n
		if err != nil {
			return created, err
		}
	}
	return created, nil
}

func (r *DictionaryRepository) putChunkIfAbsent(ctx context.Context, pending []model.DictTerm) (int, error) {
	var lastErr error
	for attempt := 0; attempt < 4 && len(pending) > 0; attempt++ {
		items := make([]types.TransactWriteItem, len(pending))
		for i := range pending {
			item, err := r.putIfAbsent(&pending[i])
			if err != nil {
				return 0, err
			}
			items[i] = item
		}
		_, lastErr = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
		var cancelled *types.TransactionCanceledException
		if lastErr == nil {
			return len(pending), nil
		} else if !errors.As(lastErr, &cancelled) {
			return 0, fmt.Errorf("failed to create dictionary terms: %w", lastErr)
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
// Account-scope callers will add a membership ConditionCheck (PR 9a/10).
func (r *DictionaryRepository) CreateAutoTerm(ctx context.Context, term *model.DictTerm, obs *model.DictObservation) error {
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
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []types.TransactWriteItem{termPut, obsPut}})
	if err != nil {
		return dictTransactError(err, []error{ErrDictTermConflict, ErrDictObservationExists})
	}
	return nil
}

// MergeAutoObservation records a new observation and bumps the auto term in
// one transaction. hits increases only when the observation row is new.
func (r *DictionaryRepository) MergeAutoObservation(ctx context.Context, in DictMergeInput) error {
	if in.Observation == nil || in.Observation.PK != in.PK || in.Observation.TermKey != in.TermKey {
		return fmt.Errorf("invalid dictionary observation merge")
	}
	obsPut, err := r.putIfAbsent(in.Observation)
	if err != nil {
		return err
	}
	condition := expression.Name("version").Equal(expression.Value(in.ExpectedVersion)).
		And(expression.Name("state").Equal(expression.Value(model.TermStateActive))).
		And(expression.Name("origin").Equal(expression.Value(model.TermOriginAuto))).
		And(expression.AttributeNotExists(expression.Name("userEditedAt")))
	update := expression.Set(expression.Name("heardAs"), expression.Value(in.HeardAs)).
		Set(expression.Name("lastSeenAt"), expression.Value(in.SeenAt)).
		Set(expression.Name("updatedAt"), expression.Value(in.SeenAt)).
		Set(expression.Name("version"), expression.Name("version").Plus(expression.Value(1))).
		Add(expression.Name("hits"), expression.Value(1))
	expr, err := expression.NewBuilder().WithUpdate(update).WithCondition(condition).Build()
	if err != nil {
		return err
	}
	items := []types.TransactWriteItem{obsPut, {Update: &types.Update{
		TableName: aws.String(r.tableName), Key: dictKey(in.PK, model.DictTermSK(in.TermKey)),
		UpdateExpression: expr.Update(), ConditionExpression: expr.Condition(),
		ExpressionAttributeNames: expr.Names(), ExpressionAttributeValues: expr.Values(),
	}}}
	_, err = r.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err != nil {
		return dictTransactError(err, []error{ErrDictObservationExists, ErrDictTermConflict})
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

func (r *DictionaryRepository) updateTerm(ctx context.Context, pk, termKey string, update expression.UpdateBuilder, condition expression.ConditionBuilder) error {
	return r.guardedUpdate(ctx, dictKey(pk, model.DictTermSK(termKey)), update, condition)
}

// UpdateDictTerm applies a user edit under a version guard and sets
// userEditedAt, which blocks every later automatic write to the term.
func (r *DictionaryRepository) UpdateDictTerm(ctx context.Context, pk, termKey string, change DictTermChange, actor, now string, expectedVersion int64) error {
	if change.State != nil && *change.State == model.TermStateDeleted {
		return fmt.Errorf("use TombstoneDictTerm to delete a dictionary term")
	}
	update := expression.Set(expression.Name("userEditedAt"), expression.Value(now)).
		Set(expression.Name("updatedBy"), expression.Value(actor)).
		Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name("version"), expression.Name("version").Plus(expression.Value(1)))
	update = setPresent(update, map[string]interface{}{
		"phrase": change.Phrase, "aliasMode": change.AliasMode,
		"type": change.Type, "state": change.State, "heardAs": change.HeardAs,
		"transcribeKo": change.TranscribeKo, "transcribeEn": change.TranscribeEn,
	})
	condition := expression.Name("version").Equal(expression.Value(expectedVersion)).
		And(expression.Name("state").NotEqual(expression.Value(model.TermStateDeleted)))
	return r.updateTerm(ctx, pk, termKey, update, condition)
}

// TombstoneDictTerm replaces a term with a tombstone so it is never
// automatically re-added, then deletes its observations best effort.
func (r *DictionaryRepository) TombstoneDictTerm(ctx context.Context, pk, termKey, actor, now string, expectedVersion int64) (DictWriteResult, error) {
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
		Remove(expression.Name("confidence"))
	condition := expression.Name("version").Equal(expression.Value(expectedVersion)).
		And(expression.Name("state").NotEqual(expression.Value(model.TermStateDeleted)))
	if err := r.updateTerm(ctx, pk, termKey, update, condition); err != nil {
		return DictWriteResult{}, err
	}
	return DictWriteResult{CleanupErr: r.deleteTermObservations(ctx, pk, termKey)}, nil
}

// RestoreDictTerm replaces a tombstone with the supplied term content. The
// stored version becomes expectedVersion+1.
func (r *DictionaryRepository) RestoreDictTerm(ctx context.Context, term *model.DictTerm, expectedVersion int64) error {
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
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.tableName), Item: av,
		ConditionExpression: expr.Condition(), ExpressionAttributeNames: expr.Names(),
		ExpressionAttributeValues: expr.Values(),
	})
	return dictSingleError(err, ErrDictTermConflict)
}

// EvictAutoTerm deletes an unedited active auto term at the expected version,
// then deletes its observations best effort.
func (r *DictionaryRepository) EvictAutoTerm(ctx context.Context, pk, termKey string, expectedVersion int64) (DictWriteResult, error) {
	condition := expression.Name("version").Equal(expression.Value(expectedVersion)).
		And(expression.Name("origin").Equal(expression.Value(model.TermOriginAuto))).
		And(expression.AttributeNotExists(expression.Name("userEditedAt"))).
		And(expression.Name("state").Equal(expression.Value(model.TermStateActive)))
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

// CreateVocabularyStateIfAbsent writes the initial VOCAB# row once.
func (r *DictionaryRepository) CreateVocabularyStateIfAbsent(ctx context.Context, state *model.VocabularyState) error {
	put, err := r.putIfAbsent(state)
	if err != nil {
		return err
	}
	_, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: put.Put.TableName, Item: put.Put.Item,
		ConditionExpression: put.Put.ConditionExpression, ExpressionAttributeNames: put.Put.ExpressionAttributeNames,
	})
	return dictSingleError(err, ErrDictTermConflict)
}

// UpdateVocabularyState applies a change to a VOCAB# row at the expected
// version and bumps the version.
func (r *DictionaryRepository) UpdateVocabularyState(ctx context.Context, userID, vocabContext, lang string, expectedVersion int64, change VocabularyStateChange, now string) error {
	update := expression.Set(expression.Name("updatedAt"), expression.Value(now)).
		Set(expression.Name("version"), expression.Name("version").Plus(expression.Value(1)))
	update = setPresent(update, map[string]interface{}{
		"activeName": change.ActiveName, "status": change.Status, "failureCode": change.FailureCode,
		"activeReady": change.ActiveReady, "termCount": change.TermCount,
	})
	return r.guardedUpdate(ctx, dictKey(model.PrefixUser+userID, model.VocabSK(vocabContext, lang)), update,
		expression.Name("version").Equal(expression.Value(expectedVersion)))
}
