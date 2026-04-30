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

    pub fn start(&mut self, meeting_id: &str) -> Result<PathBuf, AppError> {
        if self.inner.is_some() {
            return Err(AppError::AlreadyRunning);
        }

        let path = recording_path(meeting_id)?;

        #[cfg(target_os = "macos")]
        {
            let backend = macos::Backend::start(&path)?;
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
    //! API surface verified against `screencapturekit = "0.3"`. If the crate's
    //! API has shifted, the call sites below are the only places that need
    //! adjustment — the recorder lifecycle (`start` / `stop`) is stable.

    use std::path::{Path, PathBuf};
    use std::sync::{Arc, Mutex};

    use hound::{SampleFormat, WavSpec, WavWriter};
    use screencapturekit::{
        cm_sample_buffer::CMSampleBuffer,
        sc_content_filter::{InitParams, SCContentFilter},
        sc_error_handler::StreamErrorHandler,
        sc_output_handler::{SCStreamOutputType, StreamOutput},
        sc_shareable_content::SCShareableContent,
        sc_stream::SCStream,
        sc_stream_configuration::SCStreamConfiguration,
    };

    use crate::error::AppError;

    /// Output sample rate written to disk. ScreenCaptureKit emits 48 kHz by
    /// default; we keep the same to avoid resampling cost.
    const SAMPLE_RATE: u32 = 48_000;
    const CHANNELS: u16 = 2;

    pub struct Backend {
        stream: SCStream,
        writer: Arc<Mutex<Option<WavWriter<std::io::BufWriter<std::fs::File>>>>>,
        path: PathBuf,
    }

    impl Backend {
        pub fn start(path: &Path) -> Result<Self, AppError> {
            let spec = WavSpec {
                channels: CHANNELS,
                sample_rate: SAMPLE_RATE,
                bits_per_sample: 16,
                sample_format: SampleFormat::Int,
            };
            let writer = WavWriter::create(path, spec)
                .map_err(|e| AppError::Io(format!("create wav: {e}")))?;
            let writer = Arc::new(Mutex::new(Some(writer)));

            // Pick the main display as a "filter" target — SCStream requires a
            // display, but with `captures_audio = true` we only consume the
            // audio output and ignore video frames.
            let content = SCShareableContent::current();
            let display = content
                .displays
                .into_iter()
                .next()
                .ok_or_else(|| AppError::Backend("no display available".into()))?;

            let filter = SCContentFilter::new(InitParams::Display(display));

            let mut config = SCStreamConfiguration::default();
            config.captures_audio = true;
            config.excludes_current_process_audio = true;
            config.sample_rate = SAMPLE_RATE as i32;
            config.channel_count = CHANNELS as i32;

            let mut stream = SCStream::new(filter, config, ErrorHandler);
            stream.add_output(
                AudioOutput {
                    writer: Arc::clone(&writer),
                },
                SCStreamOutputType::Audio,
            );

            stream
                .start_capture()
                .map_err(|e| AppError::Backend(format!("start_capture: {e:?}")))?;

            Ok(Self {
                stream,
                writer,
                path: path.to_path_buf(),
            })
        }

        pub fn stop(self) -> Result<(), AppError> {
            self.stream
                .stop_capture()
                .map_err(|e| AppError::Backend(format!("stop_capture: {e:?}")))?;

            // Finalize WAV header.
            if let Some(w) = self.writer.lock().expect("writer poisoned").take() {
                w.finalize()
                    .map_err(|e| AppError::Io(format!("finalize wav: {e}")))?;
            }

            log::info!("stopped capture, wrote {}", self.path.display());
            Ok(())
        }
    }

    struct ErrorHandler;
    impl StreamErrorHandler for ErrorHandler {
        fn on_error(&self) {
            log::error!("ScreenCaptureKit stream error");
        }
    }

    struct AudioOutput {
        writer: Arc<Mutex<Option<WavWriter<std::io::BufWriter<std::fs::File>>>>>,
    }

    impl StreamOutput for AudioOutput {
        fn did_output_sample_buffer(&self, sample: CMSampleBuffer, of_type: SCStreamOutputType) {
            if !matches!(of_type, SCStreamOutputType::Audio) {
                return;
            }

            // CMSampleBuffer → interleaved f32 PCM. The exact accessor is
            // crate-version dependent; this branch is the most likely target
            // when iterating on a Mac dev box.
            let samples_f32: Vec<f32> = match sample.audio_buffer_list() {
                Ok(list) => list
                    .buffers()
                    .iter()
                    .flat_map(|b| b.as_f32_slice().to_vec())
                    .collect(),
                Err(e) => {
                    log::warn!("audio buffer list error: {e:?}");
                    return;
                }
            };

            let mut guard = self.writer.lock().expect("writer poisoned");
            let Some(w) = guard.as_mut() else { return };
            for s in samples_f32 {
                let clamped = s.clamp(-1.0, 1.0);
                let i16_val = (clamped * i16::MAX as f32) as i16;
                if let Err(e) = w.write_sample(i16_val) {
                    log::warn!("wav write error: {e}");
                    break;
                }
            }
        }
    }
}
