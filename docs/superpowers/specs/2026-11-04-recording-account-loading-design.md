# Recording customer selection latency

Historical design record. Status: Implemented locally; production verification
pending (2026-11-04). This document records intent, not deployed acceptance.

The user reported that the web recording preparation customer picker eventually
loads, but takes too long. Production account requests returned HTTP 200 with a
maximum logged handler duration of 251ms. The current client drains every page of
descendant discovery before showing any options; collapsed reference preparation
also mounts a second account picker and starts the same discovery.

The recording preparation picker already excludes inherited read-only customers.
It should request direct memberships without traversing descendants. Add an
optional `scope=direct` to the existing authenticated, paginated account endpoint.
Use one limited navigation index query per page, then reread canonical membership
and account metadata. Recheck every returned membership and metadata revision
before returning the page. Continuations bind the user, scope, requested page
size, last candidate and last returned revision. Index entries never grant access.

Each direct page scans at most 25 candidates. Stale index entries are omitted,
with continuation preserved even for empty pages. Legacy direct metadata retains
the existing exact-key identity contract. Default account listing and inherited
read access keep their existing discovery and continuation behavior.

Load reference preparation on its first expansion, and retain the mounted
panels when collapsed so question drafts and answers survive reopening. The
reference picker continues including inherited customers. Personal preparation
documents remain independent of customer requests, and membership changes,
retries and user changes retain their existing request-generation guards.

Deploy the API producer before the frontend consumer. Use existing private
origins and authorization; add no infrastructure or persistent client cache.
Independent automatic deployments may overlap: an initial `BAD_REQUEST` 400
for the direct scope can use legacy listing, whose recording picker still
filters direct edit access. Never fall back on authorization or continuation
errors.
