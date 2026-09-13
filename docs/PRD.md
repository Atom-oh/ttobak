# Product scope

TTOBAK reduces meeting-note work by combining recording, transcription, editable AI
notes, customer context and searchable shared material. Initial requirements were
recorded on 2026-03-05; this reference distinguishes current implementation from
historical proposals and goals. It is not a deployment/compliance certificate.

## Implemented capabilities

| Area | Current scope | Evidence |
|---|---|---|
| Identity | Admin-created Cognito users, password login/challenge/reset, token refresh, admin user management | auth components, auth-stack, user_admin service |
| Recording | Browser microphone/tab capture; macOS native system audio; pause/resume, waveform, upload/recovery | RecordButton, recording hooks, mac-app |
| Live assistance | AWS Transcribe Streaming captions, guarded Web Speech fallback, Translate, live summary and Q&A | sttManager, live hooks, QA Lambda |
| Batch transcription | Configured Whisper GPU Spot, acoustic diarization, multipart audio, AWS Transcribe fallback | transcribe/summarize commands, backend/whisper |
| Notes | Selected A/B transcript, source freshness, saved notes, generated content and action items, editing | meeting service, summarize, meeting UI |
| Images | Classification, diagram/text/table extraction and comparison | process-image, attachment UI |
| Collaboration | Personal/direct/team meeting access; account hierarchy and filters; projects with linked accounts | account/project/meeting services |
| Documents | Personal documents, account copies, read-only email shares, slide PDF preview, revocable public links | document handler, account service, convert-doc |
| Knowledge/research | KB ingestion/search, crawled news, current authorized meeting reads, research chat | crawler/research/QA artifacts |
| Simulator | User-confirmed requirements/options executed in Code Interpreter with charts/report | sim service and Python worker |
| Integrations | Notion/Markdown/Obsidian export paths and authenticated local MCP adapter | export/settings handlers, mcp-server |

Self-signup and social-login mockup buttons are not requirements. Password reset is
implemented. Nova Sonic A/B and server-side audio WebSocket designs are historical;
WebSocket currently streams QA, while live Transcribe runs in the browser.

## Access and product constraints

- Default personal ownership; sharing grants must be explicit and verified by the
  API. Account hierarchy is classification, not membership inheritance.
- Account members can add/change assignable member roles, but cannot assign owner.
  Destructive member/pending-invite actions and parent changes remain owner-only.
- Account document shares are independent copies; email shares reference the source
  and are read-only. Public document links are a separate token-based exception.
- Preserve recording even when mobile live captions fail. Uploads recover from
  stalls without imposing a fixed total transfer timeout.
- Existing accepted privacy/security limitations are recorded in CLAUDE.md and ADRs;
  they are not a claim of complete compliance or new blanket exemptions.

## Remaining limitations and historical goals

Meeting file attachments (PPTX/PDF/DOCX/MD in the meeting file category) do not get
full content extraction for summaries. AudioUploader automatically attempts KB
promotion for uploaded documents; recording-page attachments also offer manual
copy. Async ingestion and parser support are separate, and slide preview
conversion is not equivalent to summary grounding.

The current export handler's `pdf` branch is a text-download placeholder; do not
claim a generated PDF merely because the frontend button is labeled PDF. Document
PPT/PPTX-to-PDF preview conversion is implemented in a different artifact.

Mac crash leftovers lack Cognito ownership binding. Manual QA search can transmit
model-composed meeting-derived queries externally. QA TTL attributes do not imply
physical cleanup under the main table's current TTL attribute. See the precise
limits in the root guide and relevant ADRs.

Original performance targets (list under 2 seconds, autosave within 1 second after
a 3-second debounce, 10MB upload under 5 seconds, 30-minute STT under 5 minutes,
summary under 30 seconds) were product goals, not measured guarantees. Evaluate
current workload, network, Spot startup and model latency before setting SLOs.
Do not mark a requirement complete based on an old checklist or proposal alone.

## References

[Architecture](architecture.md), [API](API-SPEC.md), [UI](DESIGN-SPEC.md),
[infrastructure](INFRA-SPEC.md), and [documentation map](README.md).
