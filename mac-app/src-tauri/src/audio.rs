//! System audio capture.
//!
//! The macOS implementation uses ScreenCaptureKit (`SCStream`) with audio output
//! enabled. ScreenCaptureKit captures the audio that other apps (Zoom, Teams,
//! Chrome, …) play to the system, which is exactly what the user wants for
//! desktop meetings. No video frames are decoded — we only consume the audio
//! sample buffer output.
//!
//! Non-macOS builds provide a stub that returns `Unsupported`. This is here so
//! `cargo check` works on Linux dev machines (e.g. CI lint).

use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Instant;

use crate::error::AppError;

pub struct RecordingSnapshot {
    pub recording: bool,
    pub path: Option<PathBuf>,
    pub elapsed_ms: u64,
}

pub struct AudioRecorder {
    inner: Option<RecordingHandle>,
    /// True from `begin_start()` until `install()`/`cancel_start()` — the
    /// window during which a *reservation* exists but `inner` is still
    /// `None` because the blocking backend construction (see `begin_start`'s
    /// doc comment) is running off the lock. Without this, a second
    /// `begin_start()` during that window would see `inner.is_none()` and
    /// wrongly conclude nothing is starting.
    starting: bool,
    /// Bumped by every `begin_start()`. Passed down to each recording's
    /// `AudioOutput` (see `macos::AudioOutput`) so its callback can detect
    /// when it's been superseded by a newer recording and stop emitting
    /// events / writing samples — closes a real bug where a `stop_capture`
    /// wedged past `stop_recording`'s timeout kept running in the
    /// background (see `lib.rs`'s `STOP_CAPTURE_TIMEOUT`) and, without this
    /// guard, its `native-audio-level`/`native-pcm-chunk` events (global
    /// Tauri broadcasts, no per-recording tag) would leak into whatever
    /// recording started next.
    #[cfg_attr(not(target_os = "macos"), allow(dead_code))]
    generation: Arc<AtomicU64>,
}

/// Returned by `AudioRecorder::begin_start()`: the generation number this
/// recording was assigned, plus the shared counter so `AudioOutput` can
/// detect being superseded (see `AudioRecorder`'s `generation` field doc).
pub struct StartReservation {
    pub generation: u64,
    pub generation_counter: Arc<AtomicU64>,
}

pub struct RecordingHandle {
    pub path: PathBuf,
    pub started_at: Instant,
    #[cfg(target_os = "macos")]
    pub backend: macos::Backend,
}

impl AudioRecorder {
    pub fn new() -> Self {
        Self {
            inner: None,
            starting: false,
            generation: Arc::new(AtomicU64::new(0)),
        }
    }

    pub fn snapshot(&self) -> RecordingSnapshot {
        match &self.inner {
            Some(h) => RecordingSnapshot {
                recording: true,
                path: Some(h.path.clone()),
                elapsed_ms: h.started_at.elapsed().as_millis() as u64,
            },
            None => RecordingSnapshot {
                recording: false,
                path: None,
                elapsed_ms: 0,
            },
        }
    }

    /// Reserve this recorder for a new recording and bump the generation
    /// counter, WITHOUT doing any blocking work. Returns `AlreadyRunning` if
    /// a recording is already in progress or another `start` is still being
    /// set up.
    ///
    /// This exists because the backend construction that follows
    /// (`macos::Backend::start`) runs blocking FFI —
    /// `SCShareableContent::get()` / `stream.start_capture()` — that can
    /// block for as long as the user takes to respond to the Screen
    /// Recording permission dialog (unbounded, human-scale). The previous
    /// version of this method ran that FFI while `AudioRecorder` was held
    /// under `RecorderState.recorder`'s lock from `lib.rs`'s
    /// `start_recording` command, which froze `recording_status` (a *sync*
    /// command that runs on the app's main thread) for that whole duration —
    /// the same bug class already fixed on the stop path (see
    /// `take_handle`'s doc comment below), just never applied here.
    ///
    /// Callers MUST release the reservation via `install()` (success) or
    /// `cancel_start()` (failure) — holding it forever would wedge every
    /// future start behind a false `AlreadyRunning`.
    pub fn begin_start(&mut self) -> Result<StartReservation, AppError> {
        if self.inner.is_some() || self.starting {
            return Err(AppError::AlreadyRunning);
        }
        self.starting = true;
        let generation = self.generation.fetch_add(1, Ordering::SeqCst) + 1;
        Ok(StartReservation {
            generation,
            generation_counter: Arc::clone(&self.generation),
        })
    }

    /// Complete a `begin_start()` reservation with the backend built off the
    /// lock, installing the new recording.
    pub fn install(&mut self, path: PathBuf, #[cfg(target_os = "macos")] backend: macos::Backend) {
        self.inner = Some(RecordingHandle {
            path,
            started_at: Instant::now(),
            #[cfg(target_os = "macos")]
            backend,
        });
        self.starting = false;
    }

    /// Release a `begin_start()` reservation without installing a recording
    /// — the backend construction failed (or the platform is unsupported).
    pub fn cancel_start(&mut self) {
        self.starting = false;
    }

    /// Take the in-progress recording handle out of this recorder, if any.
    ///
    /// Deliberately does NOT stop capture or touch the WAV writer — callers
    /// (e.g. the `stop_recording` Tauri command) take the handle, drop the
    /// `RecorderState.recorder` lock, and only then run the blocking
    /// `stop_capture()` FFI call off the lock. Holding the lock across that
    /// call is what let a wedged ScreenCaptureKit stop block every other
    /// command that needs `RecorderState.recorder` (e.g. `recording_status`,
    /// which — unlike `stop_recording`/`start_recording`/`cleanup_recording`
    /// — runs as a *sync* Tauri command on the app's main thread).
    pub fn take_handle(&mut self) -> Result<RecordingHandle, AppError> {
        let handle = self.inner.take().ok_or(AppError::NotRunning)?;
        // Bump the generation at stop-REQUEST time, not just at next start:
        // the capture callbacks check it before appending samples/emitting
        // events, so a stop that wedges past STOP_CAPTURE_TIMEOUT stops
        // RECORDING system audio the moment the user asked, instead of
        // silently capturing until the next recording begins. (Everything
        // up to this instant is already in the WAV; the pending finalize
        // only flushes and closes it.)
        {
            use std::sync::atomic::Ordering;
            self.generation.fetch_add(1, Ordering::SeqCst);
        }
        Ok(handle)
    }
}

/// `pub(crate)` so `lib.rs`'s `start_recording` command can compute the path
/// before reserving/spawning the (now off-lock) backend construction — see
/// `AudioRecorder::begin_start`'s doc comment.
pub(crate) fn recording_path(meeting_id: &str) -> Result<PathBuf, AppError> {
    let safe_id: String = meeting_id
        .chars()
        .filter(|c| c.is_ascii_alphanumeric() || *c == '-' || *c == '_')
        .take(64)
        .collect();
    if safe_id.is_empty() {
        return Err(AppError::Io("invalid meeting_id".into()));
    }

    let mut dir = std::env::temp_dir();
    dir.push("ttobak-mac");
    std::fs::create_dir_all(&dir).map_err(|e| AppError::Io(format!("mkdir tmp: {e}")))?;

    let ts = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0);

    dir.push(format!("{safe_id}-{ts}.wav"));
    Ok(dir)
}

// ---------------------------------------------------------------------------
// macOS implementation
// ---------------------------------------------------------------------------
#[cfg(target_os = "macos")]
pub mod macos {
    //! ScreenCaptureKit-backed audio capture.
    //!
    //! API surface targets `screencapturekit = "1"` (1.x series).

    use std::path::{Path, PathBuf};
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::sync::{Arc, Mutex};
    use std::time::{SystemTime, UNIX_EPOCH};

    use base64::Engine;
    use hound::{SampleFormat, WavSpec, WavWriter};
    use screencapturekit::prelude::*;
    use tauri::{AppHandle, Emitter};

    use crate::error::AppError;

    /// Throttle for the `native-audio-level` event. ScreenCaptureKit delivers
    /// audio buffers at ~50 Hz (1024 frames at 48 kHz). 33 ms ≈ 30 Hz which is
    /// fast enough for a smooth meter and avoids spamming the IPC bridge.
    const LEVEL_EMIT_INTERVAL_MS: u64 = 33;

    const SAMPLE_RATE: u32 = 48_000;
    const CHANNELS: u16 = 2;

    /// Checkpoint the on-disk WAV header every ~5 seconds of audio (counted
    /// in per-channel samples, matching `samples_written`) so a force-kill
    /// loses at most ~5s instead of leaving a file whose RIFF/data size
    /// fields are still hound's zero placeholder (only patched by
    /// `finalize()`/`flush()`, both of which need the process to still be
    /// running).
    const FLUSH_INTERVAL_CHANNEL_SAMPLES: u64 = SAMPLE_RATE as u64 * CHANNELS as u64 * 5;

    /// Live-caption PCM bridge: downsample captured system audio to 16kHz
    /// mono, matching `frontend/public/pcm-processor.js`'s target rate for
    /// Amazon Transcribe Streaming (`MediaSampleRateHertz: 16000`).
    const PCM_TARGET_SAMPLE_RATE: u32 = 16_000;
    /// Chunk size in samples (~64ms at 16kHz) — mirrors the browser-mode
    /// AudioWorklet's chunking so both code paths hand Transcribe Streaming
    /// similarly-shaped audio events.
    const PCM_CHUNK_SAMPLES: usize = 1024;

    pub struct Backend {
        stream: SCStream,
        writer: Arc<Mutex<Option<WavWriter<std::io::BufWriter<std::fs::File>>>>>,
        path: PathBuf,
        /// Counts SCStream audio callbacks; used at stop() to detect silent
        /// failures where ScreenCaptureKit reports `start_capture` success
        /// but never delivers a single buffer (e.g. TCC denial after start,
        /// content filter excluding all sources, etc.).
        callbacks: Arc<AtomicU64>,
        /// Total i16 samples written to the WAV writer. Useful sanity check
        /// against `callbacks` — non-zero callbacks but zero samples means
        /// the format-conversion path rejected every buffer.
        samples_written: Arc<AtomicU64>,
    }

    impl Backend {
        /// `generation` / `generation_counter`: see `AudioRecorder`'s field
        /// doc comment in the parent module — lets `AudioOutput` detect
        /// once it's been superseded by a newer recording and stop acting.
        pub fn start(
            path: &Path,
            app: AppHandle,
            generation: u64,
            generation_counter: Arc<AtomicU64>,
        ) -> Result<Self, AppError> {
            let spec = WavSpec {
                channels: CHANNELS,
                sample_rate: SAMPLE_RATE,
                bits_per_sample: 16,
                sample_format: SampleFormat::Int,
            };
            let writer = WavWriter::create(path, spec)
                .map_err(|e| AppError::Io(format!("create wav: {e}")))?;
            let writer = Arc::new(Mutex::new(Some(writer)));

            let content = SCShareableContent::get()
                .map_err(|e| AppError::Backend(format!("SCShareableContent::get: {e:?}")))?;
            let display = content
                .displays()
                .into_iter()
                .next()
                .ok_or_else(|| AppError::Backend("no display available".into()))?;

            let filter = SCContentFilter::create()
                .with_display(&display)
                .with_excluding_windows(&[])
                .build();

            let config = SCStreamConfiguration::new()
                .with_captures_audio(true)
                .with_excludes_current_process_audio(true)
                .with_sample_rate(SAMPLE_RATE as i32)
                .with_channel_count(CHANNELS as i32);

            let callbacks = Arc::new(AtomicU64::new(0));
            let samples_written = Arc::new(AtomicU64::new(0));

            let mut stream = SCStream::new(&filter, &config);
            stream.add_output_handler(
                AudioOutput {
                    writer: Arc::clone(&writer),
                    callbacks: Arc::clone(&callbacks),
                    samples_written: Arc::clone(&samples_written),
                    logged_first: Arc::new(AtomicU64::new(0)),
                    app,
                    last_emit_ms: Arc::new(AtomicU64::new(0)),
                    since_flush: Arc::new(AtomicU64::new(0)),
                    pcm_pending: Arc::new(Mutex::new(Vec::with_capacity(PCM_CHUNK_SAMPLES * 2))),
                    my_generation: generation,
                    current_generation: generation_counter,
                },
                SCStreamOutputType::Audio,
            );

            stream
                .start_capture()
                .map_err(|e| AppError::Backend(format!("start_capture: {e:?}")))?;

            log::info!(
                "SCStream started — sample_rate={SAMPLE_RATE}Hz channels={CHANNELS} \
                 excludes_current_process_audio=true path={}",
                path.display()
            );

            Ok(Self {
                stream,
                writer,
                path: path.to_path_buf(),
                callbacks,
                samples_written,
            })
        }

        /// Stop ScreenCaptureKit capture. This is the potentially-slow part —
        /// the underlying FFI call blocks on a completion handler with no
        /// timeout of its own (see the `screencapturekit` crate's
        /// `SCStream::stop_capture`, which waits on a plain `Condvar`).
        /// Callers are expected to run this inside `spawn_blocking` raced
        /// against a timeout, NOT while holding any lock another command
        /// needs (see `AudioRecorder::take_handle`).
        pub fn stop_capture_blocking(&self) -> Result<(), AppError> {
            self.stream
                .stop_capture()
                .map_err(|e| AppError::Backend(format!("stop_capture: {e:?}")))
        }

        /// Finalize the WAV writer (patches the RIFF/data size header hound
        /// leaves as a zero placeholder until this runs). Idempotent — safe
        /// to call more than once, and safe to call whether or not
        /// `stop_capture_blocking` succeeded, timed out, or was never called
        /// at all: a partial recording is still a playable WAV once this
        /// runs at least once.
        pub fn finalize_writer(&self) -> Result<(), AppError> {
            if let Some(w) = self.writer.lock().expect("writer poisoned").take() {
                w.finalize()
                    .map_err(|e| AppError::Io(format!("finalize wav: {e}")))?;
            }
            Ok(())
        }

        /// Loud diagnostics for silent capture failures. Read-only against
        /// the atomics the audio callback maintains — safe to call any time
        /// after `finalize_writer`.
        pub fn diagnose(&self) -> Result<(), AppError> {
            let cb = self.callbacks.load(Ordering::Relaxed);
            let sw = self.samples_written.load(Ordering::Relaxed);
            let bytes = std::fs::metadata(&self.path).map(|m| m.len()).unwrap_or(0);
            log::info!(
                "stopped capture: callbacks={cb} samples_written={sw} wav_bytes={bytes} path={}",
                self.path.display()
            );

            // Hard fail loud rather than silently shipping a 44-byte empty WAV
            // header. The frontend will surface this through `onError`.
            if cb == 0 {
                return Err(AppError::Backend(
                    "ScreenCaptureKit delivered zero audio callbacks — likely a Screen \
                     Recording permission issue or an empty content filter. Reset TCC \
                     (`tccutil reset ScreenCapture click.atomai.ttobak.mac`) and relaunch."
                        .into(),
                ));
            }
            if sw == 0 {
                return Err(AppError::Backend(format!(
                    "ScreenCaptureKit delivered {cb} callbacks but no samples were written — \
                     audio buffer format probably is not f32 interleaved as assumed. Check \
                     the format-description log on the first callback and update audio.rs."
                )));
            }
            Ok(())
        }

        /// Orchestrates the full stop sequence: stop ScreenCaptureKit, THEN
        /// finalize the writer regardless of whether that stop succeeded,
        /// THEN diagnose. Finalizing unconditionally (rather than only on
        /// the success path, as the previous implementation did) closes a
        /// data-loss bug: a `stop_capture` error used to skip `finalize()`
        /// entirely, leaving a WAV with an unpatched (zero) size header even
        /// though ScreenCaptureKit may have already delivered plenty of
        /// audio.
        pub fn stop_and_finalize(&self) -> Result<(), AppError> {
            let stop_result = self.stop_capture_blocking();
            let finalize_result = self.finalize_writer();

            if let Err(e) = &finalize_result {
                log::error!("finalize_writer failed: {e}");
            }
            if let Err(e) = &stop_result {
                log::error!("stop_capture failed (finalize still ran best-effort, see above): {e}");
            }

            // Always run diagnose for its callbacks/samples/bytes triage log
            // line — most useful exactly when something above already went
            // wrong — but only let its result become this function's return
            // value when stop AND finalize both succeeded; otherwise a real
            // stop/finalize error would get silently swallowed by a
            // diagnose() that happens to pass. (Previously, a stop_capture
            // error skipped diagnose() entirely; a finalize_writer error was
            // dropped on the floor whenever stop_capture had already
            // failed.)
            let diagnose_result = self.diagnose();
            if let Err(e) = &diagnose_result {
                log::warn!("diagnose reported: {e}");
            }

            match (finalize_result, stop_result) {
                (Err(fin_err), Err(stop_err)) => Err(AppError::Backend(format!(
                    "finalize_writer failed: {fin_err} (stop_capture also failed: {stop_err})"
                ))),
                (Err(fin_err), Ok(())) => Err(fin_err),
                (Ok(()), Err(stop_err)) => Err(stop_err),
                (Ok(()), Ok(())) => diagnose_result,
            }
        }
    }

    struct AudioOutput {
        writer: Arc<Mutex<Option<WavWriter<std::io::BufWriter<std::fs::File>>>>>,
        callbacks: Arc<AtomicU64>,
        samples_written: Arc<AtomicU64>,
        /// Tracks whether we have logged the first buffer's metadata. Cheap
        /// AtomicU64 instead of `Once` so we can keep `AudioOutput: Send`.
        logged_first: Arc<AtomicU64>,
        /// Used to emit `native-audio-level` events to the WebView so the UI
        /// can show a real meter in System Audio mode (where we have no
        /// MediaStream / AnalyserNode on the JS side).
        app: AppHandle,
        last_emit_ms: Arc<AtomicU64>,
        /// Per-channel samples written since the last `writer.flush()`
        /// checkpoint. Reset (via `fetch_sub`) once it crosses
        /// `FLUSH_INTERVAL_CHANNEL_SAMPLES`.
        since_flush: Arc<AtomicU64>,
        /// 16kHz-mono samples downsampled from this callback's audio but not
        /// yet emitted as a full `PCM_CHUNK_SAMPLES`-sized `native-pcm-chunk`
        /// event. ScreenCaptureKit callback sizes don't divide evenly by the
        /// downsample ratio or the chunk size, so leftovers carry over.
        pcm_pending: Arc<Mutex<Vec<f32>>>,
        /// This recording's generation number, captured at `Backend::start`.
        my_generation: u64,
        /// Shared with `AudioRecorder` — bumped by every `start()`. If this
        /// no longer equals `my_generation`, a newer recording has started
        /// and this stream is orphaned (e.g. its `stop_capture` wedged past
        /// `stop_recording`'s timeout and kept running in the background).
        current_generation: Arc<AtomicU64>,
    }

    impl SCStreamOutputTrait for AudioOutput {
        fn did_output_sample_buffer(&self, sample: CMSampleBuffer, of_type: SCStreamOutputType) {
            if !matches!(of_type, SCStreamOutputType::Audio) {
                return;
            }

            // A newer recording has started — this stream is orphaned.
            // Stop writing AND emitting entirely: Tauri events are global
            // broadcasts with no per-recording tag, so without this check
            // an orphaned stream's `native-audio-level`/`native-pcm-chunk`
            // events would leak into whatever recording started next,
            // corrupting its waveform and feeding stale audio into its live
            // captions. (Harmless to stop writing too — the newer
            // recording always uses a fresh file path, so this stream's own
            // file was already finalized via `stop_and_finalize` or will be
            // whenever its `stop_capture` eventually returns.)
            if self.current_generation.load(Ordering::Relaxed) != self.my_generation {
                return;
            }

            self.callbacks.fetch_add(1, Ordering::Relaxed);

            // SCStreamConfiguration requests f32 interleaved PCM at 48 kHz stereo.
            // Guard: skip buffers that don't align to 4-byte f32 frames.
            let Some(list) = sample.audio_buffer_list() else {
                log::warn!("audio callback delivered no audio_buffer_list");
                return;
            };

            // Log the first buffer's shape so we can confirm the assumed
            // buffer layout against actual ScreenCaptureKit output. If the
            // user later sees a "non-empty callbacks but zero samples"
            // error, this log narrows it to a format-conversion bug.
            if self.logged_first.compare_exchange(0, 1, Ordering::Relaxed, Ordering::Relaxed).is_ok() {
                let buf_count = list.iter().count();
                let total_bytes: usize = list.iter().map(|b| b.data().len()).sum();
                let layout = if buf_count <= 1 {
                    "interleaved (single buffer)"
                } else {
                    "planar (one buffer per channel) — de-interleaving below"
                };
                log::info!(
                    "first audio buffer: buffer_count={buf_count} total_bytes={total_bytes} \
                     layout={layout} (assuming f32 → frames≈{})",
                    total_bytes / (CHANNELS as usize * 4)
                );
            }

            // ScreenCaptureKit can deliver either one interleaved buffer (all
            // channels packed together, LRLRLR…) or one buffer per channel
            // (planar — a whole mono plane per channel). Convert each raw
            // buffer to f32 first, then normalize to interleaved samples so
            // every consumer below (the WAV write pass's implicit
            // `channels: 2` framing, and `emit_pcm_chunks`'s
            // `chunks_exact(CHANNELS)` downmix) can keep assuming
            // interleaved stereo regardless of which layout this callback
            // actually used. Getting this wrong silently produces a
            // double-speed, channel-swapped recording (planar treated as
            // interleaved) — see the first-buffer log line above to confirm
            // which layout is actually in play.
            let planes: Vec<Vec<f32>> = list
                .iter()
                .map(|buf| {
                    let data = buf.data();
                    if data.len() % 4 != 0 {
                        log::warn!("unexpected audio buffer size {}, skipping", data.len());
                        return Vec::new();
                    }
                    data.chunks_exact(4)
                        .map(|c| f32::from_ne_bytes([c[0], c[1], c[2], c[3]]))
                        .collect::<Vec<_>>()
                })
                .collect();

            let samples_f32: Vec<f32> = interleave_planes(planes);

            // RMS over this buffer for the level meter event. Computed once,
            // before we move on to the WAV write and PCM downsample passes.
            let rms = if samples_f32.is_empty() {
                0.0
            } else {
                let sum_sq: f32 = samples_f32.iter().map(|s| s * s).sum();
                (sum_sq / samples_f32.len() as f32).sqrt()
            };

            // --- WAV write pass (unchanged behavior; iterates by reference
            // so `samples_f32` is still available for the PCM downsample
            // pass below) ---
            let mut guard = self.writer.lock().expect("writer poisoned");
            let mut written = 0u64;
            if let Some(w) = guard.as_mut() {
                for &s in &samples_f32 {
                    let clamped = s.clamp(-1.0, 1.0);
                    let i16_val = (clamped * i16::MAX as f32) as i16;
                    if let Err(e) = w.write_sample(i16_val) {
                        log::warn!("wav write error: {e}");
                        break;
                    }
                    written += 1;
                }

                // Periodic checkpoint: patch the RIFF/data size header now so
                // a force-kill loses at most ~5s of audio instead of leaving
                // a WAV whose header still says "0 bytes of data" (hound only
                // patches it in `flush()`/`finalize()`).
                let since_flush = self.since_flush.fetch_add(written, Ordering::Relaxed) + written;
                if since_flush >= FLUSH_INTERVAL_CHANNEL_SAMPLES {
                    match w.flush() {
                        Ok(()) => {
                            // Subtract the fixed threshold, not the observed
                            // `since_flush` snapshot: subtracting the
                            // snapshot would let two concurrent crossings
                            // both subtract their own larger total,
                            // underflowing this counter to near `u64::MAX`
                            // and forcing every later callback to flush.
                            // This is a mitigation, not a full fix, if the
                            // single-callback-thread assumption (this whole
                            // block runs under `self.writer.lock()`, which
                            // today serializes callbacks) is ever loosened —
                            // two truly concurrent crossings can still
                            // double-subtract the fixed threshold and
                            // underflow. A `fetch_update`/CAS loop would be
                            // needed to make this correct under real
                            // concurrency; this only narrows the window.
                            self.since_flush
                                .fetch_sub(FLUSH_INTERVAL_CHANNEL_SAMPLES, Ordering::Relaxed);
                        }
                        Err(e) => log::warn!("periodic wav flush failed (will retry): {e}"),
                    }
                }
            }
            drop(guard);
            self.samples_written.fetch_add(written, Ordering::Relaxed);

            // Throttled level emit (~30 Hz). Buffers arrive faster than the UI
            // needs to redraw; bouncing every callback over IPC is wasteful.
            let now_ms = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .map(|d| d.as_millis() as u64)
                .unwrap_or(0);
            let last = self.last_emit_ms.load(Ordering::Relaxed);
            if now_ms.saturating_sub(last) >= LEVEL_EMIT_INTERVAL_MS {
                self.last_emit_ms.store(now_ms, Ordering::Relaxed);
                // Map RMS (typical speech ≈ 0.01–0.3 in normalized float) to
                // a 0–1 meter range. Clamp to avoid >1 spikes from clipping.
                let level = (rms / 0.25).min(1.0);
                let _ = self.app.emit("native-audio-level", level);
            }

            // --- Live-caption PCM bridge: downsample this callback's audio
            // to 16kHz mono and emit any full chunks. Mirrors
            // `frontend/public/pcm-processor.js`'s approach (per-callback
            // linear interpolation, no fractional-position carryover across
            // callbacks — that file accepts the same tiny phase reset at
            // each buffer boundary) so both code paths feed Transcribe
            // Streaming similarly-shaped audio. ---
            self.emit_pcm_chunks(&samples_f32);
        }
    }

    impl AudioOutput {
        /// Average stereo channels to mono, linearly interpolate 48kHz →
        /// 16kHz, and emit any complete `PCM_CHUNK_SAMPLES`-sized chunk as a
        /// base64-encoded `native-pcm-chunk` event (Tauri events are JSON,
        /// so raw bytes must be encoded — a 1024-sample/64ms chunk is ~2.7KB
        /// base64, trivially small for the `evaluateJavaScript` bridge,
        /// unlike the multi-hundred-MB mistake this module used to make).
        fn emit_pcm_chunks(&self, samples_f32: &[f32]) {
            if samples_f32.len() < 2 {
                return;
            }
            let mono: Vec<f32> = samples_f32
                .chunks_exact(CHANNELS as usize)
                .map(|frame| frame.iter().sum::<f32>() / CHANNELS as f32)
                .collect();
            if mono.is_empty() {
                return;
            }

            let ratio = SAMPLE_RATE as f64 / PCM_TARGET_SAMPLE_RATE as f64;
            let out_len = (mono.len() as f64 / ratio).floor() as usize;
            if out_len == 0 {
                return;
            }

            let mut resampled = Vec::with_capacity(out_len);
            for i in 0..out_len {
                let src_index = i as f64 * ratio;
                let src_floor = src_index.floor() as usize;
                let src_ceil = (src_floor + 1).min(mono.len() - 1);
                let frac = src_index - src_floor as f64;
                let sample = mono[src_floor] as f64 * (1.0 - frac) + mono[src_ceil] as f64 * frac;
                resampled.push(sample as f32);
            }

            // Hold the lock across drain-AND-emit for every chunk in this
            // callback, rather than dropping it between chunks: each
            // payload is tiny (~2.7KB) so the emit is cheap, and holding the
            // lock is what actually guarantees chunk N is emitted before
            // chunk N+1 if this ever runs from more than one thread — SCStream
            // is documented to use a single serial callback queue today, so
            // this is defense-in-depth rather than a fix for an observed
            // reordering, but dropping the lock between drain and emit (the
            // previous shape) would have made ordering an accident of
            // scheduling instead of something this code actually enforces.
            let mut pending = self.pcm_pending.lock().expect("pcm_pending poisoned");
            pending.extend_from_slice(&resampled);

            while pending.len() >= PCM_CHUNK_SAMPLES {
                let chunk: Vec<f32> = pending.drain(..PCM_CHUNK_SAMPLES).collect();

                let mut bytes = Vec::with_capacity(PCM_CHUNK_SAMPLES * 2);
                for s in &chunk {
                    let clamped = s.clamp(-1.0, 1.0);
                    let i16_val = if clamped < 0.0 {
                        (clamped * 0x8000 as f32) as i16
                    } else {
                        (clamped * 0x7FFF as f32) as i16
                    };
                    bytes.extend_from_slice(&i16_val.to_le_bytes());
                }
                let encoded = base64::engine::general_purpose::STANDARD.encode(&bytes);
                let _ = self.app.emit("native-pcm-chunk", encoded);
            }
        }
    }

    /// Normalize ScreenCaptureKit's per-buffer f32 planes to interleaved
    /// samples. ScreenCaptureKit can deliver either one interleaved buffer
    /// (all channels packed together, LRLRLR…) or one buffer per channel
    /// (planar — a whole mono plane per channel); every consumer downstream
    /// of this function assumes interleaved stereo (the WAV write pass's
    /// implicit `channels: 2` framing in `did_output_sample_buffer`, and
    /// `AudioOutput::emit_pcm_chunks`'s `chunks_exact(CHANNELS)` downmix), so
    /// this is where that assumption is made true regardless of which
    /// layout a given callback actually used. Getting this wrong silently
    /// produces a double-speed, channel-swapped recording (planar samples
    /// treated as interleaved) — see the "first audio buffer" log line in
    /// `did_output_sample_buffer` to confirm which layout is actually in
    /// play on real hardware.
    ///
    /// Pure and free of any ScreenCaptureKit FFI types specifically so it can
    /// be unit tested (below) without a live capture session — it still only
    /// *builds and runs* on macOS, though, since it lives inside this
    /// module's `#[cfg(target_os = "macos")]` gate.
    ///
    /// Pads short/malformed planes with silence up to the LONGEST plane,
    /// rather than truncating every plane down to the shortest. A single
    /// malformed buffer (see the `data.len() % 4 != 0` guard in
    /// `did_output_sample_buffer`, which converts it to an empty plane) is a
    /// defensive, low-probability branch — but truncating-to-shortest would
    /// let that one empty plane force `frame_count` to 0 via `min()`,
    /// discarding every OTHER channel's real audio for the entire callback
    /// too. Padding instead means only the malformed channel loses that
    /// callback's audio (as silence); every good channel's audio survives.
    fn interleave_planes(planes: Vec<Vec<f32>>) -> Vec<f32> {
        match planes.len() {
            0 => Vec::new(),
            1 => planes.into_iter().next().unwrap(),
            n => {
                let lens: Vec<usize> = planes.iter().map(|p| p.len()).collect();
                let frame_count = lens.iter().copied().max().unwrap_or(0);
                if frame_count == 0 {
                    return Vec::new();
                }
                if lens.iter().any(|&l| l != frame_count) {
                    log::warn!(
                        "planar audio buffers have mismatched lengths {lens:?}, \
                         padding short/malformed planes with silence up to \
                         {frame_count} samples/plane"
                    );
                }
                let mut out = Vec::with_capacity(frame_count * n);
                for i in 0..frame_count {
                    for plane in &planes {
                        out.push(plane.get(i).copied().unwrap_or(0.0));
                    }
                }
                out
            }
        }
    }

    #[cfg(test)]
    mod interleave_tests {
        use super::interleave_planes;

        #[test]
        fn single_buffer_passes_through_unchanged_as_already_interleaved() {
            let planes = vec![vec![1.0, 2.0, 3.0, 4.0]];
            assert_eq!(interleave_planes(planes), vec![1.0, 2.0, 3.0, 4.0]);
        }

        #[test]
        fn two_planes_interleave_in_plane_order() {
            // One plane per channel (planar): L = [1,2,3], R = [10,20,30].
            // Correct interleaving is L0,R0,L1,R1,L2,R2 — NOT the buggy
            // flatten-then-treat-as-interleaved behavior this replaces,
            // which would have produced L0,L1,L2,R0,R1,R2.
            let planes = vec![vec![1.0, 2.0, 3.0], vec![10.0, 20.0, 30.0]];
            assert_eq!(
                interleave_planes(planes),
                vec![1.0, 10.0, 2.0, 20.0, 3.0, 30.0]
            );
        }

        #[test]
        fn mismatched_plane_lengths_pad_the_shorter_plane_with_silence() {
            let planes = vec![vec![1.0, 2.0, 3.0], vec![10.0, 20.0]];
            assert_eq!(
                interleave_planes(planes),
                vec![1.0, 10.0, 2.0, 20.0, 3.0, 0.0]
            );
        }

        #[test]
        fn one_empty_plane_does_not_erase_the_other_channels_audio() {
            // Regression test: a single malformed/skipped buffer (see the
            // `data.len() % 4 != 0` guard in `did_output_sample_buffer`)
            // used to zero the WHOLE callback's audio when `frame_count`
            // was computed as `min(lens)` — an empty plane forced that min
            // to 0 regardless of how much real audio the other channel(s)
            // had. Padding to `max(lens)` instead preserves it.
            let planes = vec![vec![1.0, 2.0, 3.0], vec![]];
            assert_eq!(
                interleave_planes(planes),
                vec![1.0, 0.0, 2.0, 0.0, 3.0, 0.0]
            );
        }

        #[test]
        fn empty_input_yields_empty_output() {
            let planes: Vec<Vec<f32>> = vec![];
            assert_eq!(interleave_planes(planes), Vec::<f32>::new());
        }

        #[test]
        fn three_planes_interleave_correctly() {
            // Not expected from a stereo config, but the function should
            // generalize rather than silently assume exactly 2 channels.
            let planes = vec![vec![1.0, 2.0], vec![10.0, 20.0], vec![100.0, 200.0]];
            assert_eq!(
                interleave_planes(planes),
                vec![1.0, 10.0, 100.0, 2.0, 20.0, 200.0]
            );
        }
    }
}
