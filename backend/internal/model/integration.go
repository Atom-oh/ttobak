package model

import "time"

// Integration represents external service integration settings
// PK: USER#{userId}, SK: INTEGRATION#{service}
type Integration struct {
	PK                  string    `dynamodbav:"PK"`
	SK                  string    `dynamodbav:"SK"`
	UserID              string    `dynamodbav:"userId"`
	Service             string    `dynamodbav:"service"`                       // "notion"
	APIKey              string    `dynamodbav:"apiKey"`                        // encrypted
	NotionParentID      string    `dynamodbav:"notionParentId,omitempty"`      // normalized dashed UUID
	NotionParentType    string    `dynamodbav:"notionParentType,omitempty"`    // "page_id" | "database_id"
	NotionTitleProperty string    `dynamodbav:"notionTitleProperty,omitempty"` // properties-object key for the title ("title" for a page parent, or the database's actual title-property name)
	ConfiguredAt        time.Time `dynamodbav:"configuredAt"`
	EntityType          string    `dynamodbav:"entityType"` // "INTEGRATION"
}

// Key prefix for integration records
const PrefixIntegration = "INTEGRATION#"
