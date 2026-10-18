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

// DictionaryResponse represents the API response for dictionary operations
type DictionaryResponse struct {
	Terms  []DictionaryTerm `json:"terms"`
	Status string           `json:"status"`
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

// DictTerm is one dictionary term row. A tombstone keeps only its key, state
// and timestamps so the term is never automatically re-added.
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
