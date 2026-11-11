# SFDC contact sync into account contacts

- Status: historical design record; draft for owner approval (2026-11-11).
- Follows: [Mac intake settings and SA report](2026-11-11-mac-intake-settings-and-sa-report-design.md)
  (local intake run, schedule and Collect now).
- Decisions touched: ADR-003 (local intake), ADR-021 (account contacts,
  encryption and people edges), ADR-023 (SA performance data residency).

## Intent

The owner, an AWS SA, wants the customer people recorded in SFDC to appear
as TTOBAK account contacts. There are two uses:

- matching meeting attendees and speakers to real people;
- seeing who is at a customer on the Account page.

Today account contacts come only from business cards (Remember CSV/xlsx) and
manual entry. Customer people already in SFDC are missing unless typed in.

## Owner decisions (2026-11-11)

- Both uses above.
- Only name, title and account reach TTOBAK. Email and phone are dropped on
  the Mac (decided 2026-11-10).
- Synced contacts are account contacts, visible to the Account's members
  (AM, TAM, SA). That sharing is fine.
- An existing contact is never overwritten. Only empty fields are filled.
- A person who leaves SFDC (marked no longer at the company, or removed from
  the account) is marked in TTOBAK and never deleted.

## Goals

1. **Collect.** A new intake step, `sfdc-contacts`, runs inside
   `intake run` after the existing SFDC steps, so it follows the app
   schedule, catch-up and Collect now with no new UI to start it.
   - For each SFDC-assigned account that has a TTOBAK Account mapping
     (`/api/performance/mappings`), call aws-sentral-mcp `search_contacts`
     with an `accountId` filter, following `cursor` pages.
   - Keep only customer contacts. Exclude the internal record types
     `Stakeholder Map - Internal Contacts` and `Project Resource`, and any
     contact whose email domain is `amazon.com` or another AWS domain
     (checked on the Mac and then discarded).
   - At most 200 contacts per account per run, newest-relevant first as the
     tool returns them. The rest are counted.
2. **Match on the Mac.** For each mapped Account, read its existing TTOBAK
   contacts (the existing contact list API) and decide, per SFDC contact:
   - **Same person**, when:
     - an existing contact has the same email as the SFDC contact, compared
       case-insensitively on the Mac only;
     - otherwise, an existing contact has the same normalized name and no
       conflicting email.
   - **New**, otherwise.
   - The SFDC email is used only for this comparison and is never sent,
     logged or stored.
3. **Write.**
   - **New contact:** create it with name, title and company (the TTOBAK
     Account name). Record its source as SFDC and keep the SFDC contact ID.
   - **Same person:** fill only fields that are empty in TTOBAK (title,
     company). Never change a non-empty field and never change the name. Link
     the SFDC contact ID if none is linked.
   - **Repeated runs:** these are idempotent. A contact already linked to the
     SFDC ID is updated only to fill fields that are still empty.
4. **Mark departures.** A TTOBAK contact linked to an SFDC ID that no longer
   appears in a complete per-account snapshot is marked `left SFDC` with the
   date. It is not deleted and its fields are not cleared.
   - If the person appears again, the mark is removed.
   - The mark applies only to linked contacts. Contacts the owner entered
     without a link are never marked.
   - An incomplete snapshot (a page failed, or the cap was hit) never marks
     anyone.
5. **Show.**
   - The Account contacts list shows a small "SFDC" source badge on synced
     contacts, and a "left SFDC" badge (Korean copy chosen in the UI) on marked ones.
   - Contacts are listed as today. Marked contacts sort after the others.
6. **Status.** The Mac intake settings shows the step as a fourth source,
   SFDC contacts, with its counts: fetched, created, filled, marked,
   skipped.

## Non-goals

- Writing to SFDC.
- Syncing email, phone, address or notes.
- Contacts of unmapped SFDC accounts (they wait for the mapping queue).
- Opportunity contact roles.
- Deleting or merging existing duplicate contacts.

## Anti-goals

- Overwriting anything a person typed or imported.
- Sending or storing an SFDC email or phone number, even transiently in the
  cloud.
- Deleting a contact because it left SFDC.
- Creating AWS colleagues as customer contacts.

## Constraints

- **Residency.** ADR-023 gains a row for SFDC contacts:
  - Fields: name, title, company, the SFDC contact ID, the source and the
    departure mark.
  - Visibility: account-shared like other contacts, not owner-private.
  - Rules: account contact encryption and retention (ADR-021) apply
    unchanged.
  - ADR-021 notes the new source and the fill-empty-only merge rule.
- **Existing paths.** Reuse the existing contact create and update APIs and
  their encryption, validation and revision checks. Updates are conditional
  on the observed contact revision (conditional update, no stale
  whole-item put). The builder may add one small server endpoint for a
  bounded batch "apply SFDC contacts" if per-contact calls are too slow, but
  it must reuse the same service code.
- **Authorization.** Writes require the owner's current write access to the
  Account (direct member). Accounts where the owner has only read access are
  skipped with a count.
- **Intake rules (ADR-003).**
  - Deterministic connector code, not a model.
  - Argv spawn.
  - Read tool `search_contacts` only.
  - Never log host config or contact data.
  - `AUTH_EXPIRED_MIDWAY` stops the run.
  - A failure of this step is a per-source failure (`PARTIAL_FAILURE`).
- **People edges.** Synced contacts become candidates for the existing
  people resolver like any contact. No resolver change is required.

## Designer's calls (2026-11-11, after the builder check)

- **Delete and merge.**
  - A synced contact that a member deletes is not recreated. The delete
    leaves a per-Account suppression row holding only the SFDC contact ID,
    and the sync skips suppressed IDs.
  - An Account contact merge carries the SFDC link to the surviving contact.
- **Many syncing members.** Any member with the Mac intake may run the sync;
  today only the owner does. The server enforces one contact per (Account,
  SFDC contact ID):
  - a conditional create plus a reserved index row, in the same transaction;
  - a bounded batch endpoint `POST /api/accounts/{id}/contacts/sfdc-sync`
    that reuses the contact service, encryption and revision checks;
  - a second creator gets "already linked", and fill-empty-only applies to
    it.
- **Snapshot.**
  - Departure marking for a TTOBAK Account uses the union of complete reads
    of every SFDC account mapped to it.
  - If any one of those reads is incomplete, nothing in that Account is
    marked or unmarked in that run.
  - Each link stores the SFDC account ID as well as the contact ID.
- **Cap.**
  - Departure detection reads every page of contact IDs, up to 5,000 per SFDC
    account.
  - At most 200 contacts are created or filled per Account per run; the rest
    follow on later runs.
  - Above 5,000 the read is incomplete, and nothing is marked.
- **Ambiguous name match.** When the name matches several existing contacts,
  or two SFDC contacts match one existing contact, nothing is linked or
  created for those people in that run; they are counted as `ambiguous`. An
  email match is never ambiguous, because it is one-to-one.
- **Storage.**
  - Contact fields, including the source and departure mark, stay inside the
    existing envelope encryption. The web sorts and badges after
    decryption.
  - Only the SFDC contact ID and SFDC account ID are stored in plain form, in
    the index and suppression rows. They are opaque CRM record IDs with no
    personal data.
  - Those rows follow the contact's retention: deleted with the contact,
    except the suppression row, which stays until the Account is deleted.
  - ADR-021 and ADR-023 record these rows.

## Left to the builder

- Name normalization (whitespace, case, Hangul and Latin) for the name
  match.
- Whether the step is its own sync source or part of the `sfdc` connector run.
- The exact badge UI.
- Whether the departure mark is a field on the contact or a separate row.

## Verification

- **Unit:**
  - the filter drops internal record types and AWS-domain emails;
  - an email match wins over a name match;
  - fill-empty-only never changes a non-empty field;
  - an incomplete snapshot marks nobody;
  - a reappearing contact is unmarked;
  - no request body or log line contains an email or phone number.
- **Manual on the owner's Mac:**
  - Collect now;
  - an Account page shows SFDC contacts with the badge;
  - a business-card contact with the same email is filled, not duplicated.
