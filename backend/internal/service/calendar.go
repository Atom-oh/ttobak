package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const CalendarResponseMaxBytes = 14000
const calendarMaxDuration = 93 * 24 * time.Hour

var calendarIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type CalendarStore interface {
	ApplyCalendarBatch(context.Context, string, string, repository.CalendarWriteBatch) (*model.CalendarSyncReceipt, error)
	GetCalendarBatchReceipt(context.Context, string, string, string) (*repository.CalendarBatchReceipt, error)
	ListCalendarEventRows(context.Context, string, model.CalendarQuery) (*repository.CalendarRowPage, error)
	ListCalendarSources(context.Context, string, string, int32) (*model.CalendarSourcePage, error)
}

type CalendarAccountReader interface {
	GetAccount(context.Context, string, string) (*model.AccountResponse, error)
}

type CalendarService struct {
	store    CalendarStore
	cipher   *CalendarCipher
	accounts CalendarAccountReader
	now      func() time.Time
}

func NewCalendarService(store CalendarStore, cipher *CalendarCipher, accounts CalendarAccountReader) *CalendarService {
	return &CalendarService{store: store, cipher: cipher, accounts: accounts, now: time.Now}
}

func (svc *CalendarService) Sync(ctx context.Context, userID, sourceID string, request model.CalendarSyncRequest) (*model.CalendarSyncReceipt, error) {
	if !calendarIdentifier.MatchString(userID) {
		return nil, ErrForbidden
	}
	normalizedSource, err := normalizeCalendarUUID(sourceID)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeCalendarRequest(request)
	if err != nil {
		return nil, err
	}
	digestData, err := json.Marshal(normalized)
	if err != nil {
		return nil, ErrInvalidInput
	}
	digest := sha256.Sum256(digestData)
	payloadDigest := hex.EncodeToString(digest[:])
	saved, err := svc.store.GetCalendarBatchReceipt(ctx, userID, normalizedSource, normalized.BatchID)
	if err != nil {
		return nil, err
	}
	if saved != nil && saved.ExpiresAt > svc.now().Unix() {
		if saved.UserID != userID || saved.SourceID != normalizedSource || saved.Receipt.BatchID != normalized.BatchID {
			return nil, repository.ErrCalendarCorruptData
		}
		if saved.PayloadDigest != payloadDigest {
			return nil, repository.ErrConditionFailed
		}
		return &saved.Receipt, nil
	}
	if !svc.cipher.configured() {
		return nil, ErrCalendarEncryption
	}
	accountIDs := make(map[string]bool)
	for _, mutation := range normalized.Mutations {
		if mutation.Event != nil && mutation.Event.AccountID != "" {
			accountIDs[mutation.Event.AccountID] = true
		}
	}
	if err := svc.authorizeCalendarAccounts(ctx, userID, accountIDs); err != nil {
		return nil, err
	}
	acceptedAt := svc.now().UTC()
	batch := repository.CalendarWriteBatch{
		ExpectedRevision: normalized.ExpectedRevision, PayloadDigest: payloadDigest, AcceptedAt: acceptedAt,
		WindowStart: normalized.WindowStart, WindowEnd: normalized.WindowEnd, ExpiresAt: acceptedAt.Add(7 * 24 * time.Hour).Unix(),
		Receipt:   model.CalendarSyncReceipt{BatchID: normalized.BatchID, Revision: normalized.ExpectedRevision + 1, Completed: normalized.Complete},
		Mutations: make([]repository.CalendarWriteMutation, 0, len(normalized.Mutations)),
	}
	for _, mutation := range normalized.Mutations {
		externalID := calendarMutationExternalID(mutation)
		eventID := calendarEventID(normalizedSource, externalID)
		write := repository.CalendarWriteMutation{Op: mutation.Op, ExternalID: externalID, EventID: eventID}
		if mutation.Op == "upsert" {
			event := mutation.Event
			plaintext, err := marshalCalendarPayload(*event)
			if err != nil {
				return nil, err
			}
			ciphertext, err := svc.cipher.Encrypt(ctx, userID, normalizedSource, eventID, plaintext)
			if err != nil {
				return nil, err
			}
			write.Event = &repository.CalendarEventRow{
				UserID: userID, SourceID: normalizedSource, EventID: eventID,
				StartsAt: event.StartsAt, EndsAt: event.EndsAt, AccountID: event.AccountID,
				Ciphertext: ciphertext, Revision: batch.Receipt.Revision, ExpiresAt: event.EndsAt.Add(90 * 24 * time.Hour).Unix(),
			}
			batch.Receipt.Upserted++
		} else {
			batch.Receipt.Removed++
		}
		batch.Mutations = append(batch.Mutations, write)
	}
	if err := svc.authorizeCalendarAccounts(ctx, userID, accountIDs); err != nil {
		return nil, err
	}
	return svc.store.ApplyCalendarBatch(ctx, userID, normalizedSource, batch)
}

func (svc *CalendarService) authorizeCalendarAccounts(ctx context.Context, userID string, accountIDs map[string]bool) error {
	for accountID := range accountIDs {
		if svc.accounts == nil {
			return ErrForbidden
		}
		account, err := svc.accounts.GetAccount(ctx, userID, accountID)
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
		if account == nil {
			return ErrForbidden
		}
	}
	return nil
}

func normalizeCalendarUUID(value string) (string, error) {
	if len(value) != 36 {
		return "", ErrInvalidInput
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil {
		return "", ErrInvalidInput
	}
	return parsed.String(), nil
}

func normalizeCalendarRequest(request model.CalendarSyncRequest) (model.CalendarSyncRequest, error) {
	batchID, err := normalizeCalendarUUID(request.BatchID)
	if err != nil || request.ExpectedRevision < 0 || request.ExpectedRevision == math.MaxInt64 ||
		!validCalendarInterval(request.WindowStart, request.WindowEnd) || len(request.Mutations) > 20 {
		return model.CalendarSyncRequest{}, ErrInvalidInput
	}
	normalized := request
	normalized.BatchID = batchID
	normalized.WindowStart, normalized.WindowEnd = request.WindowStart.UTC(), request.WindowEnd.UTC()
	normalized.Mutations = make([]model.CalendarMutation, 0, len(request.Mutations))
	seen := make(map[string]bool)
	for _, mutation := range request.Mutations {
		switch mutation.Op {
		case "upsert":
			if mutation.Event == nil || mutation.ExternalID != "" {
				return model.CalendarSyncRequest{}, ErrInvalidInput
			}
			event, err := normalizeCalendarEvent(*mutation.Event)
			if err != nil {
				return model.CalendarSyncRequest{}, err
			}
			mutation.Event = &event
		case "remove":
			if mutation.Event != nil || !validCalendarText(mutation.ExternalID, 512, true) {
				return model.CalendarSyncRequest{}, ErrInvalidInput
			}
		default:
			return model.CalendarSyncRequest{}, ErrInvalidInput
		}
		externalID := calendarMutationExternalID(mutation)
		if seen[externalID] {
			return model.CalendarSyncRequest{}, ErrInvalidInput
		}
		seen[externalID] = true
		normalized.Mutations = append(normalized.Mutations, mutation)
	}
	sort.Slice(normalized.Mutations, func(left, right int) bool {
		return calendarMutationExternalID(normalized.Mutations[left]) < calendarMutationExternalID(normalized.Mutations[right])
	})
	return normalized, nil
}

func normalizeCalendarEvent(event model.CalendarEventInput) (model.CalendarEventInput, error) {
	if !validCalendarText(event.ExternalID, 512, true) || !validCalendarText(event.Title, 512, true) ||
		!validCalendarText(event.Location, 256, false) || !validCalendarInterval(event.StartsAt, event.EndsAt) ||
		!validCalendarText(event.TimeZone, 128, true) || event.TimeZone == "Local" ||
		event.AccountID != "" && !calendarIdentifier.MatchString(event.AccountID) {
		return model.CalendarEventInput{}, ErrInvalidInput
	}
	location, err := time.LoadLocation(event.TimeZone)
	if err != nil {
		return model.CalendarEventInput{}, ErrInvalidInput
	}
	event.StartsAt, event.EndsAt = event.StartsAt.UTC(), event.EndsAt.UTC()
	if event.IsAllDay {
		start, startErr := time.ParseInLocation(time.DateOnly, event.StartDate, location)
		end, endErr := time.ParseInLocation(time.DateOnly, event.EndDate, location)
		if startErr != nil || endErr != nil || start.Format(time.DateOnly) != event.StartDate || end.Format(time.DateOnly) != event.EndDate ||
			!start.Equal(event.StartsAt) || !end.Equal(event.EndsAt) {
			return model.CalendarEventInput{}, ErrInvalidInput
		}
	} else if event.StartDate != "" || event.EndDate != "" {
		return model.CalendarEventInput{}, ErrInvalidInput
	}
	if err := normalizeCalendarSharing(&event); err != nil {
		return model.CalendarEventInput{}, err
	}
	if _, err := marshalCalendarPayload(event); err != nil {
		return model.CalendarEventInput{}, err
	}
	return event, nil
}

var calendarSeriesKey = regexp.MustCompile(`^[0-9a-f]{64}$`)

func normalizeCalendarSharing(event *model.CalendarEventInput) error {
	if event.ShareAccountID != "" && !calendarIdentifier.MatchString(event.ShareAccountID) ||
		len(event.CandidateAccountIDs) > 5 || len(event.ShareEvidence) > 3 ||
		event.SeriesKey != "" && !calendarSeriesKey.MatchString(event.SeriesKey) {
		return ErrInvalidInput
	}
	evidence := make(map[string]bool)
	for _, kind := range event.ShareEvidence {
		if kind != "title" && kind != "location" && kind != "domain" || evidence[kind] {
			return ErrInvalidInput
		}
		evidence[kind] = true
	}
	candidates := make(map[string]bool)
	for _, id := range event.CandidateAccountIDs {
		if !calendarIdentifier.MatchString(id) || candidates[id] || id == event.ShareAccountID {
			return ErrInvalidInput
		}
		candidates[id] = true
	}
	if len(evidence) > 0 && event.ShareAccountID == "" ||
		event.Private && (event.ShareAccountID != "" || len(candidates) > 0) {
		return ErrInvalidInput
	}
	event.ShareEvidence, event.CandidateAccountIDs = nil, nil
	for kind := range evidence {
		event.ShareEvidence = append(event.ShareEvidence, kind)
	}
	for id := range candidates {
		event.CandidateAccountIDs = append(event.CandidateAccountIDs, id)
	}
	sort.Strings(event.ShareEvidence)
	sort.Strings(event.CandidateAccountIDs)
	return nil
}

func validCalendarInterval(start, end time.Time) bool {
	return !start.IsZero() && start.UTC().Year() >= 1 && end.UTC().Year() <= 9999 &&
		end.After(start) && end.Sub(start) <= calendarMaxDuration
}

func validCalendarText(value string, maxBytes int, required bool) bool {
	return utf8.ValidString(value) && len(value) <= maxBytes && (!required || strings.TrimSpace(value) != "")
}

func marshalCalendarPayload(event model.CalendarEventInput) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(event) != nil || buffer.Len() > 4096 {
		return nil, ErrInvalidInput
	}
	return buffer.Bytes(), nil
}

func calendarMutationExternalID(mutation model.CalendarMutation) string {
	if mutation.Event != nil {
		return mutation.Event.ExternalID
	}
	return mutation.ExternalID
}

func calendarEventID(sourceID, externalID string) string {
	identity, _ := json.Marshal([]string{sourceID, externalID})
	digest := sha256.Sum256(identity)
	return hex.EncodeToString(digest[:])
}

func (svc *CalendarService) ListEvents(ctx context.Context, userID string, query model.CalendarQuery) (*model.CalendarEventPage, error) {
	if !calendarIdentifier.MatchString(userID) {
		return nil, ErrForbidden
	}
	if !validCalendarInterval(query.From, query.To) || query.Limit < 0 || query.Limit > 50 || len(query.Cursor) > 4096 ||
		query.AccountID != "" && !calendarIdentifier.MatchString(query.AccountID) {
		return nil, ErrInvalidInput
	}
	query.From, query.To = query.From.UTC(), query.To.UTC()
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.AccountID != "" {
		if err := svc.authorizeCalendarAccounts(ctx, userID, map[string]bool{query.AccountID: true}); err != nil {
			return nil, err
		}
	}
	if !svc.cipher.configured() {
		return nil, ErrCalendarEncryption
	}
	rows, err := svc.store.ListCalendarEventRows(ctx, userID, query)
	if err != nil {
		return nil, err
	}
	if rows == nil || len(rows.Rows) > int(query.Limit) {
		return nil, repository.ErrCalendarCorruptData
	}
	page := &model.CalendarEventPage{Events: make([]model.CalendarEvent, 0, len(rows.Rows)), NextCursor: rows.NextCursor}
	for _, row := range rows.Rows {
		event, err := svc.decryptCalendarEvent(ctx, userID, row)
		if err != nil {
			return nil, err
		}
		page.Events = append(page.Events, event)
	}
	access := make(map[string]bool)
	for _, event := range page.Events {
		if event.AccountID == "" {
			continue
		}
		if _, checked := access[event.AccountID]; checked {
			continue
		}
		err := svc.authorizeCalendarAccounts(ctx, userID, map[string]bool{event.AccountID: true})
		if err != nil && !errors.Is(err, ErrForbidden) {
			return nil, err
		}
		if err != nil && query.AccountID != "" {
			return nil, err
		}
		access[event.AccountID] = err == nil
	}
	for index := range page.Events {
		if accountID := page.Events[index].AccountID; accountID != "" && !access[accountID] {
			page.Events[index].AccountID = ""
		}
	}
	for !calendarResultFits(page) {
		retained := len(page.Events) - 1
		if retained <= 0 || len(rows.RowCursors) != len(rows.Rows) || rows.RowCursors[retained-1] == "" {
			return nil, repository.ErrCalendarCorruptData
		}
		page.Events = page.Events[:retained]
		cursor := rows.RowCursors[retained-1]
		page.NextCursor = &cursor
	}
	return page, nil
}

func (svc *CalendarService) ListSources(ctx context.Context, userID, cursor string, limit int32) (*model.CalendarSourcePage, error) {
	if !calendarIdentifier.MatchString(userID) {
		return nil, ErrForbidden
	}
	if limit < 0 || limit > 50 || len(cursor) > 4096 {
		return nil, ErrInvalidInput
	}
	if limit == 0 || limit > 20 {
		limit = 20
	}
	page, err := svc.store.ListCalendarSources(ctx, userID, cursor, limit)
	if err != nil {
		return nil, err
	}
	if page == nil || len(page.Sources) > int(limit) || !calendarResultFits(page) {
		return nil, repository.ErrCalendarCorruptData
	}
	return page, nil
}

func (svc *CalendarService) decryptCalendarEvent(ctx context.Context, userID string, row repository.CalendarEventRow) (model.CalendarEvent, error) {
	if row.UserID != userID || row.PK != model.PrefixUser+userID || row.SK != model.CalendarEventSortKey(row.SourceID, row.EventID) {
		return model.CalendarEvent{}, repository.ErrCalendarCorruptData
	}
	plaintext, err := svc.cipher.Decrypt(ctx, userID, row.SourceID, row.EventID, row.Ciphertext)
	if err != nil {
		return model.CalendarEvent{}, err
	}
	var input model.CalendarEventInput
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		return model.CalendarEvent{}, repository.ErrCalendarCorruptData
	}
	input, err = normalizeCalendarEvent(input)
	if err != nil || calendarEventID(row.SourceID, input.ExternalID) != row.EventID ||
		!input.StartsAt.Equal(row.StartsAt) || !input.EndsAt.Equal(row.EndsAt) || input.AccountID != row.AccountID {
		return model.CalendarEvent{}, repository.ErrCalendarCorruptData
	}
	return model.CalendarEvent{
		EventID: row.EventID, SourceID: row.SourceID, Provider: "outlook",
		Title: input.Title, StartsAt: input.StartsAt, EndsAt: input.EndsAt, TimeZone: input.TimeZone,
		IsAllDay: input.IsAllDay, StartDate: input.StartDate, EndDate: input.EndDate, Location: input.Location, AccountID: input.AccountID,
	}, nil
}

func calendarResultFits(value any) bool {
	encoded, err := json.Marshal(value)
	return err == nil && len(encoded) <= CalendarResponseMaxBytes
}
