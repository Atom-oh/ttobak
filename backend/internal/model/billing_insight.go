package model

// Billing Insight hand-off rows let a signed-in TTOBAK user open the separate
// AWS Billing Insight companion dashboard without a second sign-in. The
// companion stack owns redemption; this contract is shared with it verbatim.
//
//	PK: HANDOFF#{lowercase hex sha256(code)}, SK: HANDOFF
//	target (S) = "billing-insight", sub (S), email (S),
//	pendingShareExpiresAt (N) = issue time + BillingInsightHandoffTTLSeconds
//
// Only the hash is stored; the code itself exists only in the returned URL.
// The rows carry no GSI keys and expire through the table TTL sweep. The
// companion deletes the row on redemption (single use) and checks expiry in
// code because the sweep is not immediate.
const (
	PrefixHandoff = "HANDOFF#"
	SKHandoff     = "HANDOFF"

	BillingInsightHandoffTarget = "billing-insight"
	// BillingInsightHandoffTTLSeconds bounds how long an unredeemed code lives.
	BillingInsightHandoffTTLSeconds = 60
	// BillingInsightHandoffCodeBytes of crypto/rand, base64 RawURLEncoding,
	// yields a 43-character code.
	BillingInsightHandoffCodeBytes = 32
	// BillingInsightLoginPath is the companion's redemption route.
	BillingInsightLoginPath = "/login/ttobak"
)
