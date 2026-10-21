package model

type ConnectorApprovalState struct {
	Generation   int64    `json:"generation"`
	Capabilities []string `json:"capabilities"`
	ExpiresAt    int64    `json:"expiresAt"`
	Revoked      bool     `json:"revoked"`
	Blocked      bool     `json:"blocked"`
}

type ConnectorSettings struct {
	Profile          string                  `json:"profile"`
	ClientID         string                  `json:"clientId"`
	Enabled          bool                    `json:"enabled"`
	PolicyGeneration int64                   `json:"policyGeneration"`
	Maximum          []string                `json:"maximum"`
	Approval         *ConnectorApprovalState `json:"approval"`
}
