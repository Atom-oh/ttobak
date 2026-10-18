package model

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
	Terms  []DictionaryTerm `json:"terms"`
	Status string           `json:"status"`
	Items  []DictTermView   `json:"items,omitempty"`
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
	Item     *DictTermView `json:"item,omitempty"`
	TermID   string        `json:"termId"`
	State    string        `json:"state"`
	Status   string        `json:"status"`
	Warnings []string      `json:"warnings,omitempty"`
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

	TermOriginManual = "manual"
	TermOriginAuto   = "auto"

	TermStateActive    = "active"
	TermStateSuggested = "suggested"
	TermStateDeleted   = "deleted"

	TermScopeUser    = "user"
	TermScopeAccount = "account"

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
	CreatedBy            string   `dynamodbav:"createdBy,omitempty"`
	UpdatedBy            string   `dynamodbav:"updatedBy,omitempty"`
	Version              int64    `dynamodbav:"version"`
	EntityType           string   `dynamodbav:"entityType"`
	// LegacyMigrated marks a row copied from the legacy blob. Until
	// migratedAt such a row, while active and unedited, is only a copy: the
	// blob stays authoritative and migration may rewrite or delete it.
	LegacyMigrated bool `dynamodbav:"legacyMigrated,omitempty"`
	// LegacyDisplayEmpty records that the blob term had no displayAs, so the
	// legacy response keeps returning "" although displayAs fell back to phrase.
	LegacyDisplayEmpty bool `dynamodbav:"legacyDisplayEmpty,omitempty"`
}

// DictObservation records that one meeting produced evidence for a term. It
// is the unit of provenance; hits counts observation rows ever created.
type DictObservation struct {
	PK                string   `dynamodbav:"PK"`
	SK                string   `dynamodbav:"SK"`
	TermKey           string   `dynamodbav:"termKey"`
	MeetingID         string   `dynamodbav:"meetingId"`
	OwnerID           string   `dynamodbav:"ownerId"`
	HeardAs           []string `dynamodbav:"heardAs,omitempty"`
	SourceTypes       []string `dynamodbav:"sourceTypes,omitempty"`
	SummaryGeneration int64    `dynamodbav:"summaryGeneration"`
	NotesSafe         bool     `dynamodbav:"notesSafe"`
	CreatedAt         string   `dynamodbav:"createdAt"`
	EntityType        string   `dynamodbav:"entityType"`
}

// VocabularyState tracks the Transcribe vocabulary for one context and
// language. Status describes the latest build, not ActiveName. Build
// fingerprints and A/B slot timestamps arrive with the vocabulary builder.
type VocabularyState struct {
	PK           string `dynamodbav:"PK"`
	SK           string `dynamodbav:"SK"`
	Context      string `dynamodbav:"context"`
	Language     string `dynamodbav:"language"`
	ActiveName   string `dynamodbav:"activeName,omitempty"`
	InactiveName string `dynamodbav:"inactiveName,omitempty"`
	Status       string `dynamodbav:"status"`
	ActiveReady  bool   `dynamodbav:"activeReady"`
	FailureCode  string `dynamodbav:"failureCode,omitempty"`
	TermCount    int    `dynamodbav:"termCount"`
	UpdatedAt    string `dynamodbav:"updatedAt"`
	Version      int64  `dynamodbav:"version"`
	EntityType   string `dynamodbav:"entityType"`
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
