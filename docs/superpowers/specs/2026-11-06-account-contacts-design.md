# Account contacts and meeting speaker suggestions

Status: historical implementation design; current behavior is documented in the API and design specifications.

Customer accounts have a shared address book. Direct account members can list,
create, edit and delete contacts. Each contact has a required name and optional
company, title, department, email, address, work phone, fax, mobile phone,
business-card registration date, address book, group and notes. Contacts are independent of application
users, invitations and meeting shares. A meeting linked to an account offers the
account's team members and contacts as suggestions when the user assigns speaker
names; a person explicitly selects a name or types one. No voice identification
or automatic assignment occurs. Existing saved speaker names remain plain text.

## Access and storage

Only current direct account members may read or mutate contacts or retrieve the
team-name suggestions. An account association or meeting share alone does not
grant access. Descendant account read grants do not extend to this address book.
Check membership and account existence before reads, recheck after reads, and
atomically check both on each write so revocation cannot race an update. Keep
contacts in the account partition under `CONTACT#<uuid>`. Create conditionally;
updates and deletes require an existing contact. Bound request bodies and field
sizes. A list returns at most 25 contacts and a validated, account-bound opaque
cursor; each page independently enforces authorization.
Every contact has an opaque revision. Updates and deletes condition on the last
observed revision so concurrent editors cannot silently overwrite a change.

## Interface

Authenticated routes: `GET/POST /api/accounts/{accountId}/contacts`,
`PUT/DELETE /api/accounts/{accountId}/contacts/{contactId}`, and
`GET /api/accounts/{accountId}/speaker-team`. Contacts serialize as
`{contactId,revision,name,company,title,department,email,address,workPhone,fax,phone,cardRegisteredAt,cardBook,group,notes}`. A list returns
`{contacts,nextCursor}`. Team returns `{members:[{userId,name,email,role}],nextCursor}`,
using a member's profile name where available and email as fallback. The APIs
never log contact details. Invalid inputs return 400, denied access 403, absent
contact 404, a stale revision 409, and unexpected failures a generic 500.

Contacts persist until an account member deletes them; there is no automatic
expiry. DynamoDB point-in-time recovery can retain deleted data in backups for
the table's configured recovery window. This feature uses the existing table's
encryption and does not change its KMS key or backup policy.

The account details page has an accessible contact management section with
add/edit/delete and pagination. It is visible to direct members; loading and
mutation failures are shown. Speaker suggestions are loaded independently of
meeting access, grouped as team and customer contacts, with a way to fetch the
next contact or team page. Requests and displayed results are tied to the current
account and discarded when account, user or permissions change. If access is
denied or the service fails, manual speaker naming continues to work.

## Remember import and MCP

The account page accepts CSV and `.xlsx` files locally in the browser, limits
files to 5 MiB and 2,000 data rows. Excel archives are checked incrementally
before sheet parsing, with a 16 MiB expanded limit and bounded cell coordinates.
It previews rows before any write. Common Korean and English
headers are mapped automatically and a user can change each column mapping. The
provided Remember headers are mapped in `frontend/src/lib/contactFields.json`.
Preserve each value, subject to input bounds.
Company remains visible in the preview so the user can omit rows for other
customers before importing into the chosen account. Report successes, duplicates
and failures per row; do not silently discard unknown columns or failed rows.
Before import, compare normalized email and phone values against the full
authorized paginated contact collection and earlier accepted rows in this file;
skip duplicates and show their rows. Concurrent imports or edits can race this
client-side comparison, so do not present it as a uniqueness guarantee.
The browser does not send the file or contact data to a third party. An authenticated MCP client can
list and create account contacts through bounded structured tool calls using its
own request-scoped credentials. MCP HTTP must never accept server-local file
paths or use a shared token; the client can parse a local export and pass records.
The API remains the authorization boundary for both user interfaces. An MCP
client can send returned contact data to its configured AI provider; its own
privacy settings govern that transmission.

## Verification

Exercise direct membership and cross-account denial, revocation during writes,
missing contacts, validation and bounded pagination, team profile fallback,
account changes during asynchronous loads, and selection versus manual input.
Run backend Go tests/vet and frontend lint, unit tests and production build.
Document the new API and UI in current references. Deployment is separate.
