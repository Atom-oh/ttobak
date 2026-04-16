# Frontend Module

Next.js 16 static SPA deployed to S3/CloudFront.

## Commands

```bash
npm run dev       # local dev server (SSR, hot reload)
npm run build     # static export to out/
npm run lint      # ESLint
```

## Structure

- `src/app/` — App Router pages (record, meeting/[id], kb, files, settings, profile)
- `src/components/` — React components
  - `auth/` — LoginForm, SignUpForm, AuthProvider (Cognito)
  - `layout/` — Sidebar, DesktopHeader, MobileNav, AppLayout
  - `meeting/` — MeetingHeader, AISummaryCard, ActionItemsCard, TranscriptSection, ProcessingStatus
  - `qa/` — QAChatMessage, QAEmptyState, QASuggestedQuestions
  - `record/` — RecordingConfig, PostRecordingBanner
  - `ui/` — Skeleton
  - Root: RecordButton, LiveTranscript, LiveSummary, MeetingList, AudioPlayer, etc.
- `src/lib/` — Utilities
  - `api.ts` — apiFetch wrapper with Bearer token + refresh
  - `auth.ts` — Cognito SDK (signUp, login, refresh, getCurrentUser)
  - `sttManager.ts` — Orchestrates live STT engine switching (Web Speech / AWS Transcribe Streaming)
  - `transcribeStreamingClient.ts` — Browser-to-AWS Transcribe Streaming via `@aws-sdk/client-transcribe-streaming`
  - `speechRecognition.ts` — Web Speech API wrapper
  - `transcribeClient.ts` — Server-side transcription API calls
  - `upload.ts` — S3 presigned URL upload
  - `device.ts` — Audio input device enumeration
- `src/hooks/` — Custom hooks
  - `useRecordingSession` — MediaRecorder + chunk upload orchestration
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
- Primary colors: light `#3211d4`, dark `#00E5FF` (cyan) / `#B026FF` (purple accent)
- Responsive: mobile (`<768px`) bottom nav; desktop (`>=1024px`) sidebar `w-64`

## Gotchas

- **Tailwind v4 dark mode**: Must use `@custom-variant dark` in globals.css — without it, `dark:` utilities only respond to OS `prefers-color-scheme`, not the `.dark` class toggle
- **SPA fallback**: CloudFront 404→`/index.html` enables client-side routing for dynamic routes like `/meeting/[id]`
- **AWS SDK in browser**: `@aws-sdk/client-transcribe-streaming` runs in the browser; Cognito identity pool provides temporary credentials via `@aws-sdk/credential-providers`
- **Recording cleanup**: `RecordButton` must call `audioContextRef.current.close()` and `stream.getTracks().forEach(t => t.stop())` on stop to prevent mic lock
