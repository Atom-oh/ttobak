# Connector operation inventory

`operations.json` classifies current HTTP handlers and the assembled QA tool
registry. It is an inert policy inventory: `delegationAllowed` records intended
eligibility, not an available connector executor, caller grant or deployment.
Existing route authentication and resource authorization continue to apply.

Future approval, read-policy and read-only QA endpoints are not current bindings.
Add them with their implementation and bidirectional coverage. Elevated operations,
including sharing and deletion, remain unavailable until a separately reviewed
executor enforces the approved capability and current resource authorization.

`backend/internal/connectorinventory/inventory_test.go` checks each literal chi
route in the API AST in both directions and reads the assembled Python QA tool
registry through its AST without importing AWS clients. Missing, duplicate and
phantom bindings fail. Unsupported chi registration methods and registry-list
mutation or aliasing forms require an explicit inspector update; the check does
not execute arbitrary application code. Run `go test ./internal/connectorinventory` from `backend`
with Python 3 available. Generated language consumers must be regenerated when
this inventory changes; code readiness does not establish deployed acceptance.
