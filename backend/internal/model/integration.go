package model

import "time"

// Integration represents external service integration settings
// PK: USER#{userId}, SK: INTEGRATION#{service}
type Integration struct {
	PK           string    `dynamodbav:"PK"`
	SK           string    `dynamodbav:"SK"`
	UserID       string    `dynamodbav:"userId"`
	Service      string    `dynamodbav:"service"` // "notion"
	APIKey       string    `dynamodbav:"apiKey"`  // encrypted
	ConfiguredAt time.Time `dynamodbav:"configuredAt"`
	EntityType   string    `dynamodbav:"entityType"` // "INTEGRATION"
}

// Key prefix for integration records
const PrefixIntegration = "INTEGRATION#"
