package model

import "strings"

// DictionaryTerm represents a single term in a user's custom dictionary
type DictionaryTerm struct {
	Phrase     string `dynamodbav:"phrase" json:"phrase"`
	SoundsLike string `dynamodbav:"soundsLike" json:"soundsLike"`
	DisplayAs  string `dynamodbav:"displayAs" json:"displayAs"`
}

// UserDictionary represents a user's custom dictionary stored in DynamoDB
// PK: USER#{userId}, SK: DICTIONARY
type UserDictionary struct {
	PK               string           `dynamodbav:"PK"`
	SK               string           `dynamodbav:"SK"`
	UserID           string           `dynamodbav:"userId"`
	Terms            []DictionaryTerm `dynamodbav:"terms" json:"terms"`
	VocabularyName   string           `dynamodbav:"vocabularyName" json:"vocabularyName"`
	VocabularyStatus string           `dynamodbav:"vocabularyStatus" json:"vocabularyStatus"` // READY | PENDING | FAILED
	UpdatedAt        string           `dynamodbav:"updatedAt" json:"updatedAt"`
	EntityType       string           `dynamodbav:"entityType"` // "DICTIONARY"
	// MigratedAt is set once the blob terms have been copied into DICTTERM#
	// rows. v2 readers ignore the blob terms afterwards; the blob itself is
	// retained until every reader is on v2.
	MigratedAt string `dynamodbav:"migratedAt,omitempty" json:"-"`
	// BlobRevision is bumped by every legacy terms write so migration can
	// pin the blob it copied. Absent on blobs written before v2.
	BlobRevision int64 `dynamodbav:"blobRevision,omitempty" json:"-"`
}

// Key prefix for dictionary records
const PrefixDictionary = "DICTIONARY"

// UpdateDictionaryRequest represents the request body for updating a dictionary
type UpdateDictionaryRequest struct {
	Terms []DictionaryTerm `json:"terms"`
}

// DeleteTermRequest represents the request body for deleting a term
type DeleteTermRequest struct {
	Phrase string `json:"phrase"`
}

// DictionaryResponse represents the API response for dictionary operations.
// Terms and Status are the legacy fields; Items lists the v2 rows.
type DictionaryResponse struct {
	Terms        []DictionaryTerm `json:"terms"`
	Status       string           `json:"status"`
	Vocabularies *VocabularyViews `json:"vocabularies,omitempty"`
	Items        []DictTermView   `json:"items,omitempty"`
	// MigrationPending means the legacy list is not yet copied into rows, so
	// some legacy terms have no termId; a legacy PUT resolves it.
	MigrationPending bool     `json:"migrationPending,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
}

// DictTermView is the API form of a v2 term row. Tombstones are never listed.
type DictTermView struct {
	TermID       string   `json:"termId"`
	DisplayAs    string   `json:"displayAs"`
	Phrase       string   `json:"phrase"`
	HeardAs      []string `json:"heardAs"`
	AliasMode    string   `json:"aliasMode"`
	Type         string   `json:"type"`
	Origin       string   `json:"origin"`
	State        string   `json:"state"`
	UserEdited   bool     `json:"userEdited"`
	Version      int64    `json:"version"`
	TranscribeKo bool     `json:"transcribeKo"`
	TranscribeEn bool     `json:"transcribeEn"`
	Hits         int      `json:"hits"`
	AddedAt      string   `json:"addedAt"`
	UpdatedAt    string   `json:"updatedAt"`
}

// DictTermMutationResponse answers a v2 term create, update or delete.
type DictTermMutationResponse struct {
	Item         *DictTermView    `json:"item,omitempty"`
	TermID       string           `json:"termId"`
	State        string           `json:"state"`
	Status       string           `json:"status"`
	Vocabularies *VocabularyViews `json:"vocabularies,omitempty"`
	Warnings     []string         `json:"warnings,omitempty"`
}

// CreateDictTermRequest is the body of POST /api/settings/dictionary/terms.
// An omitted phrase defaults to displayAs, as the legacy list does.
type CreateDictTermRequest struct {
	DisplayAs string   `json:"displayAs"`
	Phrase    *string  `json:"phrase,omitempty"`
	HeardAs   []string `json:"heardAs,omitempty"`
	Type      string   `json:"type,omitempty"`
	AliasMode string   `json:"aliasMode,omitempty"`
}

// UpdateDictTermRequest is the body of PATCH .../terms/{termId}; omitted
// fields stay as they are. State may only move a suggestion to active.
type UpdateDictTermRequest struct {
	Version   *int64    `json:"version"`
	DisplayAs *string   `json:"displayAs,omitempty"`
	Phrase    *string   `json:"phrase,omitempty"`
	HeardAs   *[]string `json:"heardAs,omitempty"`
	Type      *string   `json:"type,omitempty"`
	AliasMode *string   `json:"aliasMode,omitempty"`
	State     *string   `json:"state,omitempty"`
}

// Dictionary v2 keys. Terms (DICTTERM#{termKey}) and observations
// (DICTOBS#{termKey}#{meetingId}) live under USER#{uid} or ACCOUNT#{aid};
// vocabulary state (VOCAB#{ctx}#{lang}, ctx personal|acct:{aid}) under USER#.
const (
	PrefixDictTerm = "DICTTERM#"
	PrefixDictObs  = "DICTOBS#"
	PrefixVocab    = "VOCAB#"
	// PrefixDictCommon is the partition key of the shared Common dictionary;
	// PrefixDictLog starts the SK of its per-term audit rows.
	PrefixDictCommon = "DICT#COMMON"
	PrefixDictLog    = "DICTLOG#"

	TermOriginManual = "manual"
	TermOriginAuto   = "auto"

	TermStateActive    = "active"
	TermStateSuggested = "suggested"
	TermStateDeleted   = "deleted"

	TermScopeUser    = "user"
	TermScopeAccount = "account"
	TermScopeCommon  = "common"

	// Source kinds recorded on DictSourceRef and DictObservation.
	DictSourceMeeting  = "meeting"
	DictSourceKB       = "kb"
	DictSourceResearch = "research"
	DictSourceNews     = "news"

	// MaxDictSources caps DictTerm.Sources.
	MaxDictSources = 10

	TermAliasHint   = "hint"
	TermAliasAlways = "always"

	VocabContextPersonal = "personal"
	VocabLangKo          = "ko-KR"
	VocabLangEn          = "en-US"

	VocabStatusReady         = "READY"
	VocabStatusBuilding      = "BUILDING"
	VocabStatusFailed        = "FAILED"
	VocabStatusQuotaExceeded = "QUOTA_EXCEEDED"
	VocabStatusNone          = "NONE"

	EntityTypeDictTerm        = "DICT_TERM"
	EntityTypeDictObservation = "DICT_OBSERVATION"
	EntityTypeVocabularyState = "VOCABULARY_STATE"
)

// DictTerm is one dictionary term row. A tombstone drops the term content and
// counters but keeps its (unsalted, guessable) termKey hash, origin, state and
// timestamps so the term is never automatically re-added.
type DictTerm struct {
	PK                   string   `dynamodbav:"PK"`
	SK                   string   `dynamodbav:"SK"`
	TermKey              string   `dynamodbav:"termKey"`
	Scope                string   `dynamodbav:"scope"`
	State                string   `dynamodbav:"state"`
	DisplayAs            string   `dynamodbav:"displayAs,omitempty"`
	Phrase               string   `dynamodbav:"phrase,omitempty"`
	HeardAs              []string `dynamodbav:"heardAs,omitempty"`
	AliasMode            string   `dynamodbav:"aliasMode,omitempty"`
	Type                 string   `dynamodbav:"type,omitempty"`
	Origin               string   `dynamodbav:"origin"`
	SourceTypes          []string `dynamodbav:"sourceTypes,omitempty"`
	FirstSourceMeetingID string   `dynamodbav:"firstSourceMeetingId,omitempty"`
	Hits                 int      `dynamodbav:"hits"`
	Confidence           float64  `dynamodbav:"confidence,omitempty"`
	TranscribeKo         bool     `dynamodbav:"transcribeKo"`
	TranscribeEn         bool     `dynamodbav:"transcribeEn"`
	UserEditedAt         string   `dynamodbav:"userEditedAt,omitempty"`
	AddedAt              string   `dynamodbav:"addedAt"`
	UpdatedAt            string   `dynamodbav:"updatedAt"`
	LastSeenAt           string   `dynamodbav:"lastSeenAt,omitempty"`
	DeletedAt            string   `dynamodbav:"deletedAt,omitempty"`
	// Sources lists up to MaxDictSources evidence refs, newest first.
	Sources    []DictSourceRef `dynamodbav:"sources,omitempty"`
	CreatedBy  string          `dynamodbav:"createdBy,omitempty"`
	UpdatedBy  string          `dynamodbav:"updatedBy,omitempty"`
	Version    int64           `dynamodbav:"version"`
	EntityType string          `dynamodbav:"entityType"`
	// LegacyMigrated marks a row copied from the legacy blob. Until
	// migratedAt such a row, while active and unedited, is only a copy: the
	// blob stays authoritative and migration may rewrite or delete it.
	LegacyMigrated bool `dynamodbav:"legacyMigrated,omitempty"`
	// LegacyDisplayEmpty records that the blob term had no displayAs, so the
	// legacy response keeps returning "" although displayAs fell back to phrase.
	LegacyDisplayEmpty bool `dynamodbav:"legacyDisplayEmpty,omitempty"`
}

// DictSourceRef points at one piece of evidence behind a term.
type DictSourceRef struct {
	Kind string `dynamodbav:"kind" json:"kind"`
	Ref  string `dynamodbav:"ref" json:"ref"`
	At   string `dynamodbav:"at" json:"at"`
}

// AppendDictSource puts ref first, drops an earlier entry with the same
// Kind+Ref, and keeps at most MaxDictSources. It does not modify refs.
func AppendDictSource(refs []DictSourceRef, ref DictSourceRef) []DictSourceRef {
	out := make([]DictSourceRef, 0, MaxDictSources)
	out = append(out, ref)
	for _, r := range refs {
		if len(out) == MaxDictSources {
			break
		}
		if r.Kind == ref.Kind && r.Ref == ref.Ref {
			continue
		}
		out = append(out, r)
	}
	return out
}

// DictTermLog is one Common-dictionary audit row: SK is
// DICTLOG#{termKey}#{at}. Before/After hold the term JSON ("" when absent).
type DictTermLog struct {
	PK      string `dynamodbav:"PK"`
	SK      string `dynamodbav:"SK"`
	TermKey string `dynamodbav:"termKey"`
	Actor   string `dynamodbav:"actor"`
	Action  string `dynamodbav:"action"`
	Before  string `dynamodbav:"before,omitempty"`
	After   string `dynamodbav:"after,omitempty"`
	At      string `dynamodbav:"at"`
}

// TermScopeOf maps a term partition key to its scope and, for Account, the
// account ID. ok is false for any other key.
func TermScopeOf(pk string) (scope, accountID string, ok bool) {
	switch {
	case pk == PrefixDictCommon:
		return TermScopeCommon, "", true
	case strings.HasPrefix(pk, PrefixAccount) && len(pk) > len(PrefixAccount):
		return TermScopeAccount, strings.TrimPrefix(pk, PrefixAccount), true
	case strings.HasPrefix(pk, PrefixUser) && len(pk) > len(PrefixUser):
		return TermScopeUser, "", true
	}
	return "", "", false
}

// DictObservation records that one meeting produced evidence for a term. It
// is the unit of provenance; hits counts observation rows ever created.
type DictObservation struct {
	PK        string `dynamodbav:"PK"`
	SK        string `dynamodbav:"SK"`
	TermKey   string `dynamodbav:"termKey"`
	MeetingID string `dynamodbav:"meetingId"`
	// SourceKind/SourceRef name non-meeting evidence (kb, research, news);
	// meeting rows keep meetingId.
	SourceKind        string   `dynamodbav:"sourceKind,omitempty"`
	SourceRef         string   `dynamodbav:"sourceRef,omitempty"`
	OwnerID           string   `dynamodbav:"ownerId"`
	HeardAs           []string `dynamodbav:"heardAs,omitempty"`
	SourceTypes       []string `dynamodbav:"sourceTypes,omitempty"`
	SummaryGeneration int64    `dynamodbav:"summaryGeneration"`
	NotesSafe         bool     `dynamodbav:"notesSafe"`
	// Tainted marks evidence from a run whose sources a non-owner edited
	// (A2.1); it never counts toward an active term.
	Tainted    bool   `dynamodbav:"tainted,omitempty"`
	CreatedAt  string `dynamodbav:"createdAt"`
	EntityType string `dynamodbav:"entityType"`
}

// VocabularyState tracks one context and language with two slots: ActiveName
// changes only by a flip to a READY build; InactiveName is the build target or
// the demoted slot. Status describes the latest build, not ActiveName.
type VocabularyState struct {
	PK           string `dynamodbav:"PK"`
	SK           string `dynamodbav:"SK"`
	Context      string `dynamodbav:"context"`
	Language     string `dynamodbav:"language"`
	ActiveName   string `dynamodbav:"activeName,omitempty"`
	InactiveName string `dynamodbav:"inactiveName,omitempty"`
	// RetiredName awaits deletion.
	RetiredName         string `dynamodbav:"retiredName,omitempty"`
	ActiveFingerprint   string `dynamodbav:"activeFingerprint,omitempty"`
	BuildingFingerprint string `dynamodbav:"buildingFingerprint,omitempty"`
	Status              string `dynamodbav:"status"`
	ActiveReady         bool   `dynamodbav:"activeReady"`
	FailureCode         string `dynamodbav:"failureCode,omitempty"`
	// BuildSubmitted is false while a claimed build is not yet sent.
	BuildSubmitted bool `dynamodbav:"buildSubmitted,omitempty"`
	// BypassPending lets the next claim skip the minimum interval once.
	BypassPending  bool   `dynamodbav:"bypassPending,omitempty"`
	BuildStartedAt string `dynamodbav:"buildStartedAt,omitempty"`
	// SubmittedAt is stamped by the submit claim, before the provider call; a
	// poll accepts a provider result only if modified at or after it.
	SubmittedAt string `dynamodbav:"submittedAt,omitempty"`
	FlippedAt   string `dynamodbav:"flippedAt,omitempty"`
	EmptySince  string `dynamodbav:"emptySince,omitempty"`
	TermCount   int    `dynamodbav:"termCount"`
	UpdatedAt   string `dynamodbav:"updatedAt"`
	Version     int64  `dynamodbav:"version"`
	EntityType  string `dynamodbav:"entityType"`
}

// VocabularyView is the API form of a VocabularyState.
type VocabularyView struct {
	Status      string `json:"status"`
	ActiveReady bool   `json:"activeReady"`
	FailureCode string `json:"failureCode,omitempty"`
	TermCount   int    `json:"termCount"`
}

// VocabularyViews lists the personal vocabularies by language.
type VocabularyViews struct {
	Ko VocabularyView `json:"ko"`
	En VocabularyView `json:"en"`
}

// DictTimeLayout is the fixed-width UTC timestamp used by v2 dictionary rows
// so that string order equals time order.
const DictTimeLayout = "2006-01-02T15:04:05.000000Z"

// DictTermSK returns the sort key of a term row.
func DictTermSK(termKey string) string { return PrefixDictTerm + termKey }

// DictObsSK returns the sort key of an observation row.
func DictObsSK(termKey, meetingID string) string {
	return PrefixDictObs + termKey + "#" + meetingID
}

// VocabSK returns the sort key of a vocabulary state row.
func VocabSK(vocabContext, lang string) string {
	return PrefixVocab + vocabContext + "#" + lang
}
