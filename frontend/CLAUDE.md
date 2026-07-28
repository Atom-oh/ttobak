# Frontend Module

Next.js 16 static SPA deployed to S3/CloudFront.

## Commands

```bash
npm run dev       # local dev server (SSR, hot reload)
npm run build     # static export to out/
npm run lint      # ESLint
```

## Structure

- `src/app/` — App Router pages (record, meeting/[id], accounts, accounts/[id], chat, insights, kb, files, settings, profile)
- `src/components/` — React components
  - `auth/` — LoginForm, SignUpForm, AuthProvider (Cognito)
  - `layout/` — Sidebar (incl. Accounts nav entry), DesktopHeader, MobileNav, AppLayout
  - `meeting/` — MeetingHeader, AISummaryCard, ActionItemsCard, TranscriptSection, ProcessingStatus, AccountSection (link/share-to-team)
  - `qa/` — QAChatMessage, QAEmptyState, QASuggestedQuestions
  - `record/` — RecordingConfig, PostRecordingBanner
  - `ui/` — Skeleton
  - Root: RecordButton, LiveTranscript, LiveSummary, MeetingList, AudioPlayer, AccountsClient, AccountDetailClient (members/insights/meetings/documents), etc.
- `src/lib/` — Utilities
  - `api.ts` — apiFetch wrapper with Bearer token + refresh; also `accountApi` (CRUD/members/brief) and `meetingAccountApi` (link/share-to-account)
  - `auth.ts` — Cognito SDK (signUp, login, refresh, getCurrentUser)
  - `sttManager.ts` — Orchestrates live STT engine switching (Web Speech / AWS Transcribe Streaming)
  - `transcribeStreamingClient.ts` — Browser-to-AWS Transcribe Streaming via `@aws-sdk/client-transcribe-streaming`
  - `speechRecognition.ts` — Web Speech API wrapper
  - `transcribeClient.ts` — Server-side transcription API calls
  - `upload.ts` — S3 presigned URL upload
  - `tauri.ts` — Tauri desktop app bridge: `isTauri()`, native recording commands/events (see mac-app/CLAUDE.md)
  - `device.ts` — Audio input device enumeration
- `src/hooks/` — Custom hooks
  - `useRecordingSession` — live STT session orchestration: browser modes via MediaStream (`startSession`), Tauri System Audio via Rust-pushed PCM (`startNativeSession`/`pushNativePcmChunk`, ADR-024)
  - `useLiveSummary` — Polls /api/meetings/{id}/summary during recording
  - `usePostRecording` — Post-recording status polling and finalization
  - `useAudioDevices` — Enumerate and select mic devices
- `src/types/` — TypeScript type definitions (`meeting.ts`)

## Conventions

- Static export: `output: 'export'` in production only; dev uses normal SSR
- Auth: Cognito SDK in `lib/auth.ts`, JWT in localStorage, auto-refresh on 401
- API calls: `lib/api.ts` apiFetch with Bearer token; error shape `{ error: { code, message } }`
- Styling: Tailwind v4 with `@custom-variant dark` (class-based, not media query); design tokens in `globals.css`; Material Symbols Outlined icons
- Dark mode: `.dark` class on `<html>` toggled via localStorage `theme` key; `@custom-variant dark (&:where(.dark, .dark *))` in globals.css makes all `dark:` utilities respond to the class
- Primary colors: light `#3211d4`, dark `#8b85f7` (violet) -- one unified indigo/violet brand, no separate neon palette (see root CLAUDE.md's Design System section for the full token table)
- Responsive: mobile (`<768px`) bottom nav; desktop (`>=1024px`) sidebar `w-64`

## Gotchas

- **Tailwind v4 dark mode**: Must use `@custom-variant dark` in globals.css — without it, `dark:` utilities only respond to OS `prefers-color-scheme`, not the `.dark` class toggle
- **SPA fallback**: CloudFront 404→`/index.html` enables client-side routing for dynamic routes like `/meeting/[id]`
- **AWS SDK in browser**: `@aws-sdk/client-transcribe-streaming` runs in the browser; Cognito identity pool provides temporary credentials via `@aws-sdk/credential-providers`
- **Recording cleanup**: `RecordButton` must call `audioContextRef.current.close()` and `stream.getTracks().forEach(t => t.stop())` on stop to prevent mic lock
- **Audio uploads use a progress-stall watchdog, never a fixed total timeout**: `lib/upload.ts`'s `putWithProgress` (and `mac-app/src-tauri/src/upload.rs`'s Rust-side equivalent for the Tauri desktop app) abort only after 60s with *zero progress* — a large recording on a slow-but-healthy connection must be allowed to keep going. A fixed total-request timeout here previously made large files impossible to upload regardless of connection quality (ADR-024).
- **Tauri desktop app (System Audio mode) hands `usePostRecording` a file path, not a Blob**: `RecordButton`'s native stop path never reads the recording's bytes into the WebView (see mac-app/CLAUDE.md's "Critical: don't ship WAV bytes through IPC") — it calls `onNativeFileReady(path, byteSize)` instead of `onBlobReady(blob, mimeType)`, and `usePostRecording`'s pending-upload state is a tagged union of the two. The temp WAV is deleted only after the backend's upload-complete notification succeeds, never before.
