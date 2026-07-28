# Web Tab Audio Capture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a "Tab Audio" capture mode to the Record page so users can record Google Meet audio via Chrome's getDisplayMedia API, with live STT.

**Architecture:** RecordButton gains an `audioSource` prop ('mic' | 'tab'). When 'tab', it calls `getDisplayMedia` instead of `getUserMedia` to acquire the audio stream. The rest of the pipeline (MediaRecorder, checkpoint, S3 upload, SttManager, Transcribe Streaming) receives this stream identically to mic mode. No backend changes.

**Tech Stack:** Next.js 16, TypeScript, Web APIs (getDisplayMedia, MediaRecorder, AudioContext), Tailwind v4

---

### Task 1: Add `supportsTabAudioCapture()` to device.ts

**Files:**
- Modify: `frontend/src/lib/device.ts`

- [ ] **Step 1: Add the detection function**

Add after the existing `getPreferredMimeType` function at the bottom of the file:

```typescript
export function supportsTabAudioCapture(): boolean {
  if (typeof window === 'undefined') return false;
  if (typeof navigator?.mediaDevices?.getDisplayMedia !== 'function') return false;
  return /Chrome|Edg/.test(navigator.userAgent) && !/Android/.test(navigator.userAgent);
}
```

- [ ] **Step 2: Build to verify**

```bash
cd /home/ec2-user/ttobak/frontend && npm run build 2>&1 | tail -5
```

- [ ] **Step 3: Commit**

```bash
cd /home/ec2-user/ttobak && git add frontend/src/lib/device.ts
git commit -m "feat(device): add supportsTabAudioCapture detection for Chrome/Edge

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Add `audioSource` prop to RecordButton

**Files:**
- Modify: `frontend/src/components/RecordButton.tsx`

- [ ] **Step 1: Add audioSource to RecordButtonProps**

In the `RecordButtonProps` interface (around line 8), add:

```typescript
audioSource?: 'mic' | 'tab';
```

Add it to the destructured props in the component function (around line 33):

```typescript
export function RecordButton({
  meetingId, meetingTitle, deviceId, audioSource = 'mic', onRecordingComplete, ...
}: RecordButtonProps)
```

- [ ] **Step 2: Add getDisplayMedia stream acquisition**

Import `supportsTabAudioCapture` at the top:

```typescript
import { isIOS, getPreferredMimeType, supportsMediaRecorder, supportsTabAudioCapture } from '@/lib/device';
```

In the `startRecording` function, replace the `getUserMedia` call (around line 136) with source-dependent logic:

```typescript
let stream: MediaStream;
if (audioSource === 'tab') {
  try {
    stream = await navigator.mediaDevices.getDisplayMedia({
      video: { width: 1, height: 1 },
      audio: true,
    });
    // Discard dummy video track immediately
    stream.getVideoTracks().forEach(t => t.stop());
    // Verify we got an audio track
    if (stream.getAudioTracks().length === 0) {
      onError?.('선택한 탭에서 오디오를 캡처할 수 없습니다');
      return;
    }
  } catch (err: unknown) {
    if (err instanceof DOMException && err.name === 'NotAllowedError') {
      return; // User cancelled tab picker — do nothing
    }
    throw err;
  }
} else {
  const audioConstraints: MediaTrackConstraints | boolean = deviceId
    ? { deviceId: { exact: deviceId } }
    : true;
  stream = await navigator.mediaDevices.getUserMedia({ audio: audioConstraints });
}
```

- [ ] **Step 3: Add track.onended handler for tab sharing stop**

After the stream is acquired (both mic and tab paths), before creating MediaRecorder, add:

```typescript
if (audioSource === 'tab') {
  stream.getAudioTracks()[0].onended = () => {
    // Tab sharing was stopped externally (Chrome "Stop sharing" button or tab closed)
    if (isRecordingRef.current) {
      stopRecording();
    }
  };
}
```

Where `isRecordingRef` is a ref that tracks recording state (add `const isRecordingRef = useRef(false);` in the component, set `true` in startRecording, `false` in stopRecording/cleanup).

- [ ] **Step 4: Build to verify**

```bash
cd /home/ec2-user/ttobak/frontend && npm run build 2>&1 | tail -5
```

- [ ] **Step 5: Commit**

```bash
cd /home/ec2-user/ttobak && git add frontend/src/components/RecordButton.tsx
git commit -m "feat(record): add tab audio capture via getDisplayMedia in RecordButton

Supports audioSource='tab' prop for Chrome tab audio capture.
Handles: tab picker cancel, no audio track, external stop sharing.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Add Audio Source Selector UI to Record Page

**Files:**
- Modify: `frontend/src/app/record/page.tsx`

- [ ] **Step 1: Add audioSource state**

Add state near the other useState calls (around line 38):

```typescript
const [audioSource, setAudioSource] = useState<'mic' | 'tab'>('mic');
const [tabSharingLabel, setTabSharingLabel] = useState<string | null>(null);
```

Import `supportsTabAudioCapture`:

```typescript
import { supportsTabAudioCapture } from '@/lib/device';
```

- [ ] **Step 2: Add Audio Source Selector segment buttons**

In the idle config section (around line 379, inside the `!isUploadMode && !postRecording.step && !session.isRecording` block), add BEFORE the MicSelector:

```tsx
{/* Audio Source Selector */}
{supportsTabAudioCapture() && (
  <div className="flex flex-col items-center gap-2 w-full max-w-xs">
    <span className="text-xs font-semibold text-slate-500 dark:text-[#849396] uppercase tracking-wide">
      Audio Source
    </span>
    <div className="flex rounded-lg border border-slate-200 dark:border-white/10 overflow-hidden w-full">
      <button
        onClick={() => setAudioSource('mic')}
        className={`flex-1 flex items-center justify-center gap-1.5 px-4 py-2 text-sm font-semibold transition-colors ${
          audioSource === 'mic'
            ? 'bg-primary text-white dark:text-[#09090E]'
            : 'text-slate-600 dark:text-[#849396] hover:bg-slate-50 dark:hover:bg-white/5'
        }`}
      >
        <span className="material-symbols-outlined text-base">mic</span>
        Mic
      </button>
      <button
        onClick={() => setAudioSource('tab')}
        className={`flex-1 flex items-center justify-center gap-1.5 px-4 py-2 text-sm font-semibold transition-colors ${
          audioSource === 'tab'
            ? 'bg-primary text-white dark:text-[#09090E]'
            : 'text-slate-600 dark:text-[#849396] hover:bg-slate-50 dark:hover:bg-white/5'
        }`}
      >
        <span className="material-symbols-outlined text-base">tab</span>
        Tab Audio
      </button>
    </div>
  </div>
)}
```

- [ ] **Step 3: Conditionally show MicSelector vs Tab Audio info**

Wrap the existing `MicSelector` in a mic-mode condition, and add tab-mode info:

```tsx
{audioSource === 'mic' && (
  <MicSelector devices={devices} selectedDeviceId={selectedDeviceId} onSelect={selectDevice}
               disabled={session.isRecording} analyser={session.isRecording ? analyserNode : previewAnalyser} />
)}
{audioSource === 'tab' && !session.isRecording && (
  <div className="flex items-center gap-2 px-4 py-2 bg-blue-50 dark:bg-blue-900/10 border border-blue-200 dark:border-blue-500/20 rounded-lg text-sm text-blue-700 dark:text-blue-300">
    <span className="material-symbols-outlined text-base">info</span>
    Record 버튼을 누르면 공유할 탭을 선택할 수 있습니다
  </div>
)}
{audioSource === 'tab' && session.isRecording && tabSharingLabel && (
  <div className="flex items-center gap-2 px-4 py-2 bg-green-50 dark:bg-green-900/10 border border-green-200 dark:border-green-500/20 rounded-lg text-sm text-green-700 dark:text-green-300">
    <span className="material-symbols-outlined text-base">volume_up</span>
    Sharing: {tabSharingLabel}
  </div>
)}
```

- [ ] **Step 4: Pass audioSource to RecordButton**

Update the RecordButton rendering (around line 425) to include the new prop:

```tsx
<RecordButton
  meetingId={clientMeetingId}
  meetingTitle={meetingTitle || 'Untitled Meeting'}
  deviceId={audioSource === 'mic' ? (selectedDeviceId || undefined) : undefined}
  audioSource={audioSource}
  onRecordingComplete={postRecording.handleRecordingComplete}
  ...
```

- [ ] **Step 5: Update handleRecordingStart to capture tab label**

In `handleRecordingStart` (around line 136), add tab label detection:

```typescript
const handleRecordingStart = async (stream: MediaStream) => {
  summary.reset();
  await postRecording.createDraftMeeting();

  // Capture tab sharing label from the audio track
  if (audioSource === 'tab') {
    const label = stream.getAudioTracks()[0]?.label || 'Tab Audio';
    setTabSharingLabel(label);
  }

  session.startSession(() => {
    previewStreamRef.current?.getTracks().forEach((t) => t.stop());
    ...
    setPreviewAnalyser(null);
  }, stream);
};
```

- [ ] **Step 6: Clear tab label on stop**

In the recording stop/cleanup handler, add:

```typescript
setTabSharingLabel(null);
```

- [ ] **Step 7: Disable mic preview in tab mode**

In the mic preview useEffect (around line 89), add a guard:

```typescript
useEffect(() => {
  if (audioSource !== 'mic') return; // Skip mic preview in tab mode
  if (session.isRecording || !selectedDeviceId) { ... }
  ...
}, [selectedDeviceId, session.isRecording, audioSource]);
```

- [ ] **Step 8: Build to verify**

```bash
cd /home/ec2-user/ttobak/frontend && npm run build 2>&1 | tail -5
```

- [ ] **Step 9: Commit**

```bash
cd /home/ec2-user/ttobak && git add frontend/src/app/record/page.tsx
git commit -m "feat(record): add Mic/Tab Audio source selector with tab sharing status

Audio source segment buttons (Mic/Tab Audio), conditional MicSelector,
tab info banner before recording, green sharing badge during recording.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Wire Tab Audio Stream to Live STT

**Files:**
- Modify: `frontend/src/lib/sttManager.ts` (minor — verify stream acceptance)
- Modify: `frontend/src/hooks/useRecordingSession.ts` (minor — verify stream passthrough)

- [ ] **Step 1: Verify SttManager accepts any MediaStream**

Read `sttManager.ts` line 49-54. The `start(stream: MediaStream, ...)` method already accepts any `MediaStream` — it doesn't call `getUserMedia` itself. The tab audio stream will work without modification because `TranscribeStreamingSession.start(stream)` just needs a `MediaStream` with audio tracks.

**No code changes needed in sttManager.ts.** The existing interface already supports tab audio streams.

- [ ] **Step 2: Verify useRecordingSession passes stream through**

Read `useRecordingSession.ts` line 180. The `startSession(cleanup, stream)` method passes the stream directly to `manager.start(stream, ...)`. No filtering or mic-specific logic.

**No code changes needed in useRecordingSession.ts.** The stream passthrough is source-agnostic.

- [ ] **Step 3: Verify TranscribeStreamingSession accepts any stream**

The `TranscribeStreamingSession.start(stream)` in `transcribeStreamingClient.ts` creates an `AudioContext` from the stream and routes it through an `AudioWorklet`. It doesn't care whether the stream comes from `getUserMedia` or `getDisplayMedia` — it just needs audio tracks.

**No code changes needed in transcribeStreamingClient.ts.**

- [ ] **Step 4: Commit verification note**

```bash
cd /home/ec2-user/ttobak && git commit --allow-empty -m "chore: verify STT pipeline accepts tab audio stream (no changes needed)

SttManager.start(), useRecordingSession.startSession(), and
TranscribeStreamingSession.start() all accept any MediaStream.
Tab audio stream from getDisplayMedia works without modification.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Build, Deploy, and Test

**Files:**
- No new changes — deploy what's been built

- [ ] **Step 1: Build frontend**

```bash
cd /home/ec2-user/ttobak/frontend && npm run build
```

Expected: Build succeeds, `/record` page included in output.

- [ ] **Step 2: Deploy frontend to S3 + CloudFront**

```bash
aws s3 sync /home/ec2-user/ttobak/frontend/out/ s3://ttobak-site-180294183052-ap-northeast-2/ --delete
aws cloudfront create-invalidation --distribution-id E3IFMH57E9UTB5 --paths "/*"
```

- [ ] **Step 3: Test in Chrome**

Manual test checklist:
1. Open `https://ttobak.atomai.click/record`
2. Verify: Mic/Tab Audio segment buttons visible (Chrome/Edge only)
3. Select "Tab Audio" mode
4. Verify: MicSelector hidden, info banner shown
5. Click Record → Chrome tab picker appears
6. Select a tab playing audio (e.g., YouTube or Google Meet)
7. Verify: Recording starts, green "Sharing: {tab name}" badge visible
8. Verify: Live transcript populates from tab audio
9. Click Stop → recording uploads, redirects to meeting detail
10. Verify: Transcript and summary generated from tab audio

Additional tests:
- Cancel tab picker → verify stays on page, no error
- Stop sharing via Chrome bar → verify recording auto-stops and uploads
- Switch to Mic mode → verify existing mic recording still works
- Open in Safari/Firefox → verify Tab Audio button is hidden

- [ ] **Step 4: Final commit**

```bash
cd /home/ec2-user/ttobak && git add -A
git commit -m "feat: complete tab audio capture for Google Meet recording

Tab Audio mode added to Record page. Uses getDisplayMedia to capture
Chrome tab audio for recording + live STT. No backend changes.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```
