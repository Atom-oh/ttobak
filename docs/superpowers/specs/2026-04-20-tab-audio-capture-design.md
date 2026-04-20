# Web Tab Audio Capture Design (Sub-project 1)

## Overview

Add a "Tab Audio" capture mode to the Record page so users can record Google Meet (and other browser-based meeting tools) by capturing the audio output of a Chrome tab via `getDisplayMedia`. This enables meeting recording without requiring the other party to share a recording file.

Sub-project 2 (Tauri Mac App for system-level audio capture including Zoom desktop app) will be designed separately.

## Requirements

- Add audio source selector: Microphone (existing) / Tab Audio (new)
- Tab Audio mode uses `getDisplayMedia({audio: true})` to capture a Chrome tab's audio
- Captured tab audio feeds into the existing MediaRecorder (S3 upload) AND Transcribe Streaming (live STT)
- No backend changes — same S3 → EventBridge → Transcribe → Summarize pipeline
- Tab Audio button hidden on unsupported browsers (Safari, Firefox)
- Tab sharing end (user clicks "Stop sharing" or closes tab) auto-stops recording
- Chrome 94+, Edge 94+ supported

## Architecture

### Audio Stream Pipeline

```
User clicks Record (Tab Audio mode)
    ↓
getDisplayMedia({video: {width:1,height:1}, audio: true})
    ↓ (Chrome shows tab picker → user selects Meet tab)
tabAudioStream.getVideoTracks()[0].stop()  // discard dummy video
    ↓
tabAudioStream (audio-only MediaStream)
    ↓
AudioContext.createMediaStreamSource(tabAudioStream)
    ↓
MediaStreamAudioDestinationNode
    ├─→ MediaRecorder
    │     mimeType: audio/webm;codecs=opus
    │     timeslice: 1000ms
    │     60s checkpoint → S3 (existing flow)
    │     onStop → final upload → notifyComplete → Transcribe pipeline
    │
    └─→ AudioWorklet (pcm-processor.js)
          ↓ PCM 16kHz mono
          TranscribeStreamingSession (live captions)
```

### Comparison with Existing Mic Mode

| Component | Mic Mode (existing) | Tab Audio Mode (new) |
|-----------|-------------------|---------------------|
| Stream source | `getUserMedia({audio})` | `getDisplayMedia({audio})` |
| MediaRecorder | Same | Same (different stream source) |
| Checkpoint upload | Same | Same |
| Final S3 upload | Same | Same |
| notifyComplete | Same | Same |
| SttManager | mic stream → AudioWorklet | tab stream → AudioWorklet |
| Transcribe Lambda | Same | Same |
| Summarize Lambda | Same | Same |
| Backend changes | - | None |

The only code change is the stream acquisition method. Everything downstream is identical.

## UI Changes

### Record Page — Audio Source Selector

Location: Above the record button, below the meeting title input.

```
┌─────────────────────────────────────────┐
│  Audio Source                            │
│  [ 🎤 Mic ]  [ 🔊 Tab Audio ]          │  ← segment buttons
│                                          │
│  Mic mode:                               │
│    Device: [Built-in Microphone ▼]       │
│                                          │
│  Tab Audio mode:                         │
│    ℹ️ Click Record to select a tab       │  (before capture)
│    🔊 Sharing: meet.google.com           │  (during capture)
└─────────────────────────────────────────┘
```

### States

**Before recording (Tab Audio selected)**:
- Info text: "Record 버튼을 누르면 공유할 탭을 선택할 수 있습니다"
- Record button label: "Start Tab Recording"

**During recording (Tab Audio active)**:
- Green badge: "🔊 Sharing: {tab title}" 
- Stop button: same as current mic recording stop
- Live transcript: displays tab audio captions

**Tab sharing ended externally** (user clicks Chrome's "Stop sharing"):
- Auto-triggers stopRecording()
- Same post-recording flow (upload, redirect to meeting detail)

### Browser Support Detection

```typescript
function supportsTabAudioCapture(): boolean {
  return typeof navigator.mediaDevices?.getDisplayMedia === 'function'
    && /Chrome|Edg/.test(navigator.userAgent);
}
```

Tab Audio button is hidden when `supportsTabAudioCapture()` returns false.

## File Changes

### Modified Files

**`frontend/src/components/RecordButton.tsx`**
- Add `audioSource: 'mic' | 'tab'` prop
- In `startRecording()`: if audioSource is 'tab', call `getDisplayMedia` instead of `getUserMedia`
- Create `AudioContext` + `MediaStreamAudioDestinationNode` to route tab stream to both MediaRecorder and STT
- Add `track.onended` handler for external tab sharing stop
- Stop dummy video track immediately after getDisplayMedia

**`frontend/src/app/record/page.tsx`**
- Add `audioSource` state: `'mic' | 'tab'`
- Render segment button pair (Mic / Tab Audio)
- Pass `audioSource` to `RecordButton` and `useRecordingSession`
- Show device selector only in mic mode
- Show tab sharing status in tab mode

**`frontend/src/hooks/useRecordingSession.ts`**
- Accept `audioSource` parameter
- When tab mode: pass tab audio stream to SttManager instead of mic stream

**`frontend/src/lib/sttManager.ts`**
- Accept external `MediaStream` for STT input (currently creates its own from mic)
- Route provided stream to AudioWorklet → Transcribe Streaming

**`frontend/src/lib/device.ts`**
- Add `supportsTabAudioCapture()` function

### New Files

None. All changes are modifications to existing files.

### Backend Changes

None. Zero backend changes required.

## getDisplayMedia Specifics

### Chrome Tab Audio Behavior

When `getDisplayMedia({audio: true})` is called and user selects a Chrome tab:
- Audio track contains ALL audio output of that tab (e.g., all participants in Google Meet)
- User's own microphone is NOT included (the user's mic goes through Meet directly to other participants)
- If user mutes in Meet, their voice stops being sent to others but has no effect on Ttobak capture
- Tab audio continues even if Ttobak tab is in background

### video: false Gotcha

`getDisplayMedia({video: false, audio: true})` is rejected by some Chrome versions. Workaround:

```typescript
const stream = await navigator.mediaDevices.getDisplayMedia({
  video: { width: 1, height: 1 },  // minimal dummy video
  audio: true,
});
// Immediately discard video track
stream.getVideoTracks().forEach(t => t.stop());
```

### Self-cancellation

If user dismisses the tab picker (clicks Cancel):
```typescript
try {
  const stream = await navigator.mediaDevices.getDisplayMedia({...});
} catch (err) {
  if (err.name === 'NotAllowedError') {
    // User cancelled — do nothing, stay on record page
    return;
  }
  throw err;
}
```

## Error Handling

| Scenario | Behavior |
|----------|----------|
| User cancels tab picker | Stay on record page, no error shown |
| Tab selected but no audio track | Show error: "선택한 탭에서 오디오를 캡처할 수 없습니다" |
| Tab sharing stops during recording | Auto-stop recording, proceed to upload |
| getDisplayMedia not supported | Tab Audio button hidden |
| Tab audio + STT connection fails | STT falls back to disabled, recording continues |

## Testing Plan

- Chrome: select Meet tab → verify audio captured and transcribed
- Chrome: cancel tab picker → verify no error, stays on page
- Chrome: stop sharing mid-recording → verify clean stop and upload
- Edge: same as Chrome tests
- Safari/Firefox: verify Tab Audio button is hidden
- Mobile: verify Tab Audio button is hidden (getDisplayMedia not supported)
- Verify existing mic recording still works unchanged

## Out of Scope

- System audio capture for desktop apps (Zoom) — Sub-project 2 (Tauri Mac App)
- Audio mixing (tab + mic) — not needed since Meet includes user's mic in tab audio
- Screen video recording — audio only
- Firefox/Safari support — Chrome/Edge only for tab audio
