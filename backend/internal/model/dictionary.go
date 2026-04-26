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
	Terms          []DictionaryTerm `json:"terms"`
	Status         string           `json:"status"`
	VocabularyName string           `json:"vocabularyName,omitempty"`
}
