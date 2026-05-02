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
use std::time::Instant;

use crate::error::AppError;

pub struct RecordingSummary {
    pub path: PathBuf,
    pub duration_ms: u64,
    pub byte_size: u64,
}

pub struct RecordingSnapshot {
    pub recording: bool,
    pub path: Option<PathBuf>,
    pub elapsed_ms: u64,
}

pub struct AudioRecorder {
    inner: Option<RecordingHandle>,
}

struct RecordingHandle {
    path: PathBuf,
    started_at: Instant,
    #[cfg(target_os = "macos")]
    backend: macos::Backend,
}

impl AudioRecorder {
    pub fn new() -> Self {
        Self { inner: None }
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

    pub fn start(
        &mut self,
        meeting_id: &str,
        #[cfg(target_os = "macos")] app: tauri::AppHandle,
        #[cfg(not(target_os = "macos"))] _app: tauri::AppHandle,
    ) -> Result<PathBuf, AppError> {
        if self.inner.is_some() {
            return Err(AppError::AlreadyRunning);
        }

        let path = recording_path(meeting_id)?;

        #[cfg(target_os = "macos")]
        {
            let backend = macos::Backend::start(&path, app)?;
            self.inner = Some(RecordingHandle {
                path: path.clone(),
                started_at: Instant::now(),
                backend,
            });
            Ok(path)
        }

        #[cfg(not(target_os = "macos"))]
        {
            let _ = path;
            Err(AppError::Unsupported)
        }
    }

    pub fn stop(&mut self) -> Result<RecordingSummary, AppError> {
        let handle = self.inner.take().ok_or(AppError::NotRunning)?;
        let duration_ms = handle.started_at.elapsed().as_millis() as u64;

        #[cfg(target_os = "macos")]
        {
            handle.backend.stop()?;
        }

        let byte_size = std::fs::metadata(&handle.path)
            .map(|m| m.len())
            .unwrap_or(0);

        Ok(RecordingSummary {
            path: handle.path,
            duration_ms,
            byte_size,
        })
    }
}

fn recording_path(meeting_id: &str) -> Result<PathBuf, AppError> {
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
mod macos {
    //! ScreenCaptureKit-backed audio capture.
    //!
    //! API surface targets `screencapturekit = "1"` (1.x series).

    use std::path::{Path, PathBuf};
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::sync::{Arc, Mutex};
    use std::time::{SystemTime, UNIX_EPOCH};

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
        pub fn start(path: &Path, app: AppHandle) -> Result<Self, AppError> {
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

        pub fn stop(self) -> Result<(), AppError> {
            self.stream
                .stop_capture()
                .map_err(|e| AppError::Backend(format!("stop_capture: {e:?}")))?;

            if let Some(w) = self.writer.lock().expect("writer poisoned").take() {
                w.finalize()
                    .map_err(|e| AppError::Io(format!("finalize wav: {e}")))?;
            }

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
    }

    impl SCStreamOutputTrait for AudioOutput {
        fn did_output_sample_buffer(&self, sample: CMSampleBuffer, of_type: SCStreamOutputType) {
            if !matches!(of_type, SCStreamOutputType::Audio) {
                return;
            }

            self.callbacks.fetch_add(1, Ordering::Relaxed);

            // SCStreamConfiguration requests f32 interleaved PCM at 48 kHz stereo.
            // Guard: skip buffers that don't align to 4-byte f32 frames.
            let Some(list) = sample.audio_buffer_list() else {
                log::warn!("audio callback delivered no audio_buffer_list");
                return;
            };

            // Log the first buffer's shape so we can confirm the f32 assumption
            // against actual ScreenCaptureKit output. If the user later sees
            // a "non-empty callbacks but zero samples" error, this log narrows
            // it to a format-conversion bug.
            if self.logged_first.compare_exchange(0, 1, Ordering::Relaxed, Ordering::Relaxed).is_ok() {
                let buf_count = list.iter().count();
                let total_bytes: usize = list.iter().map(|b| b.data().len()).sum();
                log::info!(
                    "first audio buffer: buffer_count={buf_count} total_bytes={total_bytes} \
                     (assuming f32 interleaved → frames={})",
                    total_bytes / (CHANNELS as usize * 4)
                );
            }

            let samples_f32: Vec<f32> = list
                .iter()
                .flat_map(|buf| {
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

            // RMS over this buffer for the level meter event. Computed once,
            // before we move samples_f32 into the writer loop.
            let rms = if samples_f32.is_empty() {
                0.0
            } else {
                let sum_sq: f32 = samples_f32.iter().map(|s| s * s).sum();
                (sum_sq / samples_f32.len() as f32).sqrt()
            };

            let mut guard = self.writer.lock().expect("writer poisoned");
            let Some(w) = guard.as_mut() else { return };
            let mut written = 0u64;
            for s in samples_f32 {
                let clamped = s.clamp(-1.0, 1.0);
                let i16_val = (clamped * i16::MAX as f32) as i16;
                if let Err(e) = w.write_sample(i16_val) {
                    log::warn!("wav write error: {e}");
                    break;
                }
                written += 1;
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
        }
    }
}
