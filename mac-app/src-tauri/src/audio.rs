//! System audio + microphone capture.
//!
//! The macOS implementation (`mod macos`) records a Core Audio process tap of
//! every other app's output (Zoom, Teams, Chrome, …) mixed with the default
//! microphone, through one private aggregate device (macOS 14.2+; see
//! ADR-046). Channel splitting, mixing and caption resampling are pure
//! functions in `crate::mix` so they are unit tested on every platform.
//!
//! Non-macOS builds provide a stub that returns `Unsupported`, so `cargo
//! check`/`cargo test` run on Linux dev machines; only the Core Audio backend
//! inside `mod macos` is `cfg`-gated to a real Mac.

use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Instant;

use parking_lot::Mutex;

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
    #[cfg_attr(not(target_os = "macos"), allow(dead_code))]
    pub generation: u64,
    #[cfg_attr(not(target_os = "macos"), allow(dead_code))]
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
    /// tap/aggregate creation and `AudioDeviceStart` — that can block for as
    /// long as the user takes to answer the Microphone / System Audio
    /// Recording permission dialogs (unbounded, human-scale). The previous
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
    /// lock, installing the new recording. Only called from `lib.rs`'s
    /// `cfg(target_os = "macos")` block, hence the dead_code allowance on
    /// other targets (same convention as `interleave_planes`).
    #[cfg_attr(not(target_os = "macos"), allow(dead_code))]
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
    /// call is what let a wedged capture stop block every other
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

/// RAII release for a `begin_start()` reservation.
///
/// `start_recording` (lib.rs) holds its reservation across an `.await`
/// (the off-lock `spawn_blocking` backend construction). Every failure path
/// used to call `cancel_start()` by hand — four sites, easy to miss when a
/// new one is added — and a command future dropped at that `.await` (Tauri
/// normally runs async commands to completion, but nothing guarantees it)
/// would have released nothing at all, leaving `starting = true` forever and
/// every later start wedged behind a false `AlreadyRunning`. The guard makes
/// release unconditional: drop it without `disarm()` and the reservation is
/// cancelled; call `disarm()` after `install()` and it does nothing.
///
/// The guard takes the recorder lock inside `Drop`. `parking_lot::Mutex` is
/// NOT reentrant, so a guard must never be dropped while its owner is still
/// holding `RecorderState.recorder` — keep every lock in the owning function
/// a temporary or a tightly scoped block, as `start_recording` already does.
pub struct StartGuard<'a> {
    recorder: &'a Mutex<AudioRecorder>,
    armed: bool,
}

impl<'a> StartGuard<'a> {
    /// Arm a guard for a reservation just taken via `begin_start()` on
    /// `recorder`. The caller must have already released the lock it used
    /// for `begin_start()`.
    pub fn new(recorder: &'a Mutex<AudioRecorder>) -> Self {
        Self {
            recorder,
            armed: true,
        }
    }

    /// The reservation was consumed by `install()`; nothing to cancel.
    /// Only reached from `lib.rs`'s `cfg(target_os = "macos")` block, hence
    /// the dead_code allowance on other targets.
    #[cfg_attr(not(target_os = "macos"), allow(dead_code))]
    pub fn disarm(mut self) {
        self.armed = false;
    }
}

impl Drop for StartGuard<'_> {
    fn drop(&mut self) {
        if self.armed {
            self.recorder.lock().cancel_start();
        }
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
    //! Core Audio process-tap capture (macOS 14.2+) mixed with the default
    //! microphone in one private aggregate device (ADR-046): the microphone
    //! is the clock sub-device, the tap is drift-compensated, and one IOProc
    //! gets the mic streams followed by the tap stream. Without an input
    //! device the default output clocks a system-only recording. The
    //! real-time IOProc only mixes and enqueues; a worker thread writes the
    //! WAV and emits events. Teardown: stop the device, destroy the IOProc,
    //! aggregate and tap, then close the queue and join the worker.

    use std::ffi::{c_void, CStr};
    use std::io::BufWriter;
    use std::path::{Path, PathBuf};
    use std::ptr::NonNull;
    use std::sync::atomic::{AtomicU32, AtomicU64, Ordering};
    use std::sync::mpsc::{sync_channel, Receiver, SyncSender, TrySendError};
    use std::sync::{Arc, Mutex};
    use std::thread::JoinHandle;
    use std::time::{SystemTime, UNIX_EPOCH};

    use base64::Engine;
    use core_foundation::array::CFArray;
    use core_foundation::base::{CFType, TCFType};
    use core_foundation::boolean::CFBoolean;
    use core_foundation::dictionary::CFDictionary;
    use core_foundation::string::CFString;
    use hound::{SampleFormat, WavSpec, WavWriter};
    use objc2::AllocAnyThread;
    use objc2_core_audio::{
        kAudioAggregateDeviceIsPrivateKey, kAudioAggregateDeviceIsStackedKey,
        kAudioAggregateDeviceMainSubDeviceKey, kAudioAggregateDeviceNameKey,
        kAudioAggregateDeviceSubDeviceListKey, kAudioAggregateDeviceTapAutoStartKey,
        kAudioAggregateDeviceTapListKey, kAudioAggregateDeviceUIDKey,
        kAudioDevicePropertyDeviceUID, kAudioDevicePropertyNominalSampleRate,
        kAudioDevicePropertyStreamConfiguration, kAudioHardwarePropertyDefaultInputDevice,
        kAudioHardwarePropertyDefaultOutputDevice,
        kAudioHardwarePropertyTranslatePIDToProcessObject, kAudioObjectPropertyElementMain,
        kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyScopeInput, kAudioObjectSystemObject,
        kAudioSubDeviceDriftCompensationKey, kAudioSubDeviceUIDKey,
        kAudioSubTapDriftCompensationKey, kAudioSubTapUIDKey, AudioDeviceCreateIOProcID,
        AudioDeviceDestroyIOProcID, AudioDeviceIOProcID, AudioDeviceStart, AudioDeviceStop,
        AudioHardwareCreateAggregateDevice, AudioHardwareCreateProcessTap,
        AudioHardwareDestroyAggregateDevice, AudioHardwareDestroyProcessTap,
        AudioObjectGetPropertyData, AudioObjectGetPropertyDataSize, AudioObjectID,
        AudioObjectPropertyAddress, AudioObjectPropertyScope, AudioObjectPropertySelector,
        CATapDescription, CATapMuteBehavior,
    };
    use objc2_core_audio_types::{AudioBuffer, AudioBufferList, AudioTimeStamp};
    use objc2_foundation::{NSArray, NSNumber, NSString};
    use tauri::{AppHandle, Emitter};

    use crate::error::AppError;
    use crate::mix::{self, BufferView, ChannelLayout, Resampler};

    /// Throttle for the `native-audio-level` event (~30 Hz).
    const LEVEL_EMIT_INTERVAL_MS: u64 = 33;
    /// The WAV is always interleaved stereo: system L/R with the microphone
    /// mixed into both channels (see `mix::mix`).
    const CHANNELS: u16 = 2;
    /// Checkpoint the WAV header every ~5 seconds of audio so a force-kill
    /// loses at most that much (hound patches sizes only in flush/finalize).
    const FLUSH_INTERVAL_SECONDS: u64 = 5;
    /// Live-caption PCM: 16 kHz mono, ~64 ms chunks, matching
    /// `frontend/public/pcm-processor.js` and Transcribe Streaming.
    const PCM_TARGET_SAMPLE_RATE: f64 = 16_000.0;
    const PCM_CHUNK_SAMPLES: usize = 1024;
    /// IOProc → worker buffer queue. At a typical 512-frame IO buffer this is
    /// several seconds of slack before a stalled disk drops audio (counted and
    /// reported at stop, never silently).
    const QUEUE_BUFFERS: usize = 1024;
    /// Used when the aggregate does not report a usable nominal rate.
    const FALLBACK_SAMPLE_RATE: u32 = 48_000;

    type Wav = WavWriter<BufWriter<std::fs::File>>;

    /// State shared with the real-time IOProc through its client-data
    /// pointer. Lives in a `Box` owned by `Resources` until the IOProc has
    /// been destroyed.
    struct CallbackCtx {
        tx: Mutex<Option<SyncSender<Vec<f32>>>>,
        layout: ChannelLayout,
        stats: Arc<Stats>,
        my_generation: u64,
        current_generation: Arc<AtomicU64>,
    }

    #[derive(Default)]
    struct Stats {
        callbacks: AtomicU64,
        samples_written: AtomicU64,
        dropped_buffers: AtomicU64,
        logged_first: AtomicU64,
        /// f32 bit patterns of non-negative peaks: monotonic under `fetch_max`.
        system_peak_bits: AtomicU32,
        mic_peak_bits: AtomicU32,
    }

    impl Stats {
        fn system_peak(&self) -> f32 {
            f32::from_bits(self.system_peak_bits.load(Ordering::Relaxed))
        }
        fn mic_peak(&self) -> f32 {
            f32::from_bits(self.mic_peak_bits.load(Ordering::Relaxed))
        }
    }

    /// Core Audio objects created for one recording. `Drop` releases them in
    /// dependency order, so an error midway through `Backend::start` cleans
    /// up whatever already exists.
    struct Resources {
        tap: AudioObjectID,
        aggregate: AudioObjectID,
        proc_id: AudioDeviceIOProcID,
        started: bool,
        ctx: *mut CallbackCtx,
    }

    // The raw pointers are only dereferenced by the IOProc (Core Audio's
    // thread) until teardown reclaims them; `Resources` itself is moved
    // between threads but never shared.
    unsafe impl Send for Resources {}

    impl Resources {
        fn empty() -> Self {
            Self {
                tap: 0,
                aggregate: 0,
                proc_id: None,
                started: false,
                ctx: std::ptr::null_mut(),
            }
        }

        /// Stops and destroys everything; returns the first failing call.
        ///
        /// The callback context is freed only once the IOProc is confirmed
        /// detached (device stopped and IOProc destroyed). If either call
        /// fails, the HAL may still invoke the IOProc, so the context and the
        /// device objects are deliberately leaked; only the queue is closed,
        /// which ends the worker and makes any later callback a no-op.
        fn teardown(&mut self) -> Result<(), AppError> {
            let fail = |what: &str, status: i32| {
                AppError::Backend(format!("{what} failed: OSStatus {status}"))
            };
            unsafe {
                if self.started {
                    // Synchronous from a non-IO thread: returns after any
                    // in-flight IOProc invocation has finished.
                    let status = AudioDeviceStop(self.aggregate, self.proc_id);
                    if status != 0 {
                        self.abandon();
                        return Err(fail("AudioDeviceStop", status));
                    }
                    self.started = false;
                }
                if self.proc_id.is_some() {
                    let status = AudioDeviceDestroyIOProcID(self.aggregate, self.proc_id);
                    if status != 0 {
                        self.abandon();
                        return Err(fail("AudioDeviceDestroyIOProcID", status));
                    }
                    self.proc_id = None;
                }
                // Detached: nothing can call the IOProc any more.
                if !self.ctx.is_null() {
                    let ctx = Box::from_raw(self.ctx);
                    self.ctx = std::ptr::null_mut();
                    // Closing the sender ends the worker's receive loop.
                    ctx.tx.lock().map(|mut tx| tx.take()).ok();
                }
                let mut first_err = None;
                if self.aggregate != 0 {
                    let status = AudioHardwareDestroyAggregateDevice(self.aggregate);
                    if status != 0 {
                        first_err = Some(fail("AudioHardwareDestroyAggregateDevice", status));
                    }
                    self.aggregate = 0;
                }
                if self.tap != 0 {
                    let status = AudioHardwareDestroyProcessTap(self.tap);
                    if status != 0 && first_err.is_none() {
                        first_err = Some(fail("AudioHardwareDestroyProcessTap", status));
                    }
                    self.tap = 0;
                }
                first_err.map_or(Ok(()), Err)
            }
        }

        /// Detachment failed: close the queue through the still-valid context
        /// and forget every handle, leaking the context and device objects so
        /// a late IOProc call never touches freed memory.
        fn abandon(&mut self) {
            if !self.ctx.is_null() {
                // SAFETY: never freed on this path, so the pointer stays valid.
                let ctx = unsafe { &*self.ctx };
                ctx.tx.lock().map(|mut tx| tx.take()).ok();
            }
            log::error!("Core Audio IOProc detachment failed; leaking its context and devices");
            self.ctx = std::ptr::null_mut();
            self.started = false;
            self.proc_id = None;
            self.aggregate = 0;
            self.tap = 0;
        }
    }

    impl Drop for Resources {
        fn drop(&mut self) {
            if let Err(e) = self.teardown() {
                log::error!("Core Audio teardown on drop: {e}");
            }
        }
    }

    pub struct Backend {
        resources: Mutex<Option<Resources>>,
        worker: Mutex<Option<JoinHandle<()>>>,
        writer: Arc<Mutex<Option<Wav>>>,
        path: PathBuf,
        stats: Arc<Stats>,
        mic_active: bool,
        start_warnings: Vec<String>,
    }

    impl Backend {
        /// `generation` / `generation_counter`: see `AudioRecorder`'s field
        /// doc comment in the parent module — lets the IOProc detect it has
        /// been superseded by a newer recording and stop acting.
        pub fn start(
            path: &Path,
            app: AppHandle,
            generation: u64,
            generation_counter: Arc<AtomicU64>,
        ) -> Result<Self, AppError> {
            let mut res = Resources::empty();
            let mut start_warnings = Vec::new();

            // 1. Tap of every process except this app (its own UI sounds).
            let own = own_process_object();
            if own.is_none() {
                start_warnings.push(
                    "Could not identify this app's audio process; its own sounds may be recorded."
                        .into(),
                );
            }
            let exclude: Vec<objc2::rc::Retained<NSNumber>> = own
                .into_iter()
                .map(NSNumber::numberWithUnsignedInt)
                .collect();
            let exclude = NSArray::from_retained_slice(&exclude);
            let desc = unsafe {
                CATapDescription::initStereoGlobalTapButExcludeProcesses(
                    CATapDescription::alloc(),
                    &exclude,
                )
            };
            unsafe {
                desc.setPrivate(true);
                desc.setMuteBehavior(CATapMuteBehavior::Unmuted);
                desc.setName(&NSString::from_str("TTOBAK system audio"));
            }
            let mut tap: AudioObjectID = 0;
            check("AudioHardwareCreateProcessTap", unsafe {
                AudioHardwareCreateProcessTap(Some(&desc), &mut tap)
            })?;
            res.tap = tap;
            let tap_uid = unsafe { desc.UUID().UUIDString() }.to_string();

            // 2. Clock/sub-device: the default microphone when present.
            let input = default_device(kAudioHardwarePropertyDefaultInputDevice)
                .and_then(|id| device_uid(id).map(|uid| (id, uid)));
            let (main_uid, layout, mic_active) = match input {
                Some((id, uid)) if input_channels(id) > 0 => (
                    uid,
                    ChannelLayout {
                        skip: 0,
                        mic: input_channels(id),
                    },
                    true,
                ),
                _ => {
                    start_warnings.push(
                        "No microphone input device is available; recording system audio only."
                            .into(),
                    );
                    let out = default_device(kAudioHardwarePropertyDefaultOutputDevice)
                        .ok_or_else(|| AppError::Backend("no default output device".into()))?;
                    let uid = device_uid(out).ok_or_else(|| {
                        AppError::Backend("default output device has no UID".into())
                    })?;
                    // A headset output can carry its own input streams; they
                    // precede the tap in the buffer list and must be skipped.
                    (
                        uid,
                        ChannelLayout {
                            skip: input_channels(out),
                            mic: 0,
                        },
                        false,
                    )
                }
            };

            let aggregate_uid = format!(
                "click.atomai.ttobak.mac.capture.{}.{}",
                std::process::id(),
                now_ms()
            );
            let sub_device = cf_dict(&[
                (kAudioSubDeviceUIDKey, CFString::new(&main_uid).as_CFType()),
                (
                    kAudioSubDeviceDriftCompensationKey,
                    CFBoolean::false_value().as_CFType(),
                ),
            ]);
            let sub_tap = cf_dict(&[
                (kAudioSubTapUIDKey, CFString::new(&tap_uid).as_CFType()),
                (
                    kAudioSubTapDriftCompensationKey,
                    CFBoolean::true_value().as_CFType(),
                ),
            ]);
            let description = cf_dict(&[
                (
                    kAudioAggregateDeviceUIDKey,
                    CFString::new(&aggregate_uid).as_CFType(),
                ),
                (
                    kAudioAggregateDeviceNameKey,
                    CFString::new("TTOBAK Capture").as_CFType(),
                ),
                (
                    kAudioAggregateDeviceIsPrivateKey,
                    CFBoolean::true_value().as_CFType(),
                ),
                (
                    kAudioAggregateDeviceIsStackedKey,
                    CFBoolean::false_value().as_CFType(),
                ),
                (
                    kAudioAggregateDeviceTapAutoStartKey,
                    CFBoolean::true_value().as_CFType(),
                ),
                (
                    kAudioAggregateDeviceMainSubDeviceKey,
                    CFString::new(&main_uid).as_CFType(),
                ),
                (
                    kAudioAggregateDeviceSubDeviceListKey,
                    CFArray::from_CFTypes(&[sub_device]).as_CFType(),
                ),
                (
                    kAudioAggregateDeviceTapListKey,
                    CFArray::from_CFTypes(&[sub_tap]).as_CFType(),
                ),
            ]);
            let mut aggregate: AudioObjectID = 0;
            check("AudioHardwareCreateAggregateDevice", unsafe {
                // core-foundation's CFDictionaryRef and objc2's CFDictionary
                // are the same toll-free CF object.
                let dict = &*(description.as_concrete_TypeRef()
                    as *const objc2_core_foundation::CFDictionary);
                AudioHardwareCreateAggregateDevice(dict, NonNull::from(&mut aggregate))
            })?;
            res.aggregate = aggregate;
            // Aggregates list sub-device input streams first and append tap
            // streams after them; `ChannelLayout` relies on that order.
            let total_channels = input_channels(aggregate);
            if total_channels < layout.skip + layout.mic + 1 {
                log::warn!(
                    "aggregate reports {total_channels} input channels for layout {layout:?}; \
                     the tap stream may be missing"
                );
            }

            let sample_rate = get_f64(
                aggregate,
                kAudioDevicePropertyNominalSampleRate,
                kAudioObjectPropertyScopeGlobal,
            )
            .filter(|r| r.is_finite() && *r >= 8_000.0 && *r <= 384_000.0)
            .map(|r| r.round() as u32)
            .unwrap_or_else(|| {
                start_warnings.push(format!(
                    "Could not read the capture sample rate; assuming {FALLBACK_SAMPLE_RATE} Hz \
                     (playback speed may be wrong)."
                ));
                FALLBACK_SAMPLE_RATE
            });

            // 3. Writer + worker before the IOProc can deliver anything.
            let spec = WavSpec {
                channels: CHANNELS,
                sample_rate,
                bits_per_sample: 16,
                sample_format: SampleFormat::Int,
            };
            let writer = WavWriter::create(path, spec)
                .map_err(|e| AppError::Io(format!("create wav: {e}")))?;
            let writer = Arc::new(Mutex::new(Some(writer)));
            let stats = Arc::new(Stats::default());
            let (tx, rx) = sync_channel::<Vec<f32>>(QUEUE_BUFFERS);
            let worker = spawn_worker(
                rx,
                Arc::clone(&writer),
                Arc::clone(&stats),
                app,
                sample_rate,
                generation,
                Arc::clone(&generation_counter),
            )?;

            let ctx = Box::into_raw(Box::new(CallbackCtx {
                tx: Mutex::new(Some(tx)),
                layout,
                stats: Arc::clone(&stats),
                my_generation: generation,
                current_generation: generation_counter,
            }));
            res.ctx = ctx;

            // 4. IOProc, then start. Starting the aggregate raises the
            // Microphone / System Audio Recording permission prompts once.
            let mut proc_id: AudioDeviceIOProcID = None;
            let started = (|| {
                check("AudioDeviceCreateIOProcID", unsafe {
                    AudioDeviceCreateIOProcID(
                        aggregate,
                        Some(io_proc),
                        ctx.cast(),
                        NonNull::from(&mut proc_id),
                    )
                })?;
                res.proc_id = proc_id;
                check("AudioDeviceStart", unsafe {
                    AudioDeviceStart(aggregate, proc_id)
                })?;
                res.started = true;
                Ok::<(), AppError>(())
            })();
            if let Err(e) = started {
                drop(res); // tears down and closes the channel
                let _ = worker.join();
                if let Ok(mut w) = writer.lock() {
                    w.take();
                }
                let _ = std::fs::remove_file(path);
                return Err(e);
            }

            log::info!(
                "Core Audio capture started — rate={sample_rate}Hz layout={layout:?} mic={mic_active} path={}",
                path.display()
            );
            Ok(Self {
                resources: Mutex::new(Some(res)),
                worker: Mutex::new(Some(worker)),
                writer,
                path: path.to_path_buf(),
                stats,
                mic_active,
                start_warnings,
            })
        }

        /// Warnings to surface in the start response (e.g. no microphone).
        pub fn start_warnings(&self) -> Vec<String> {
            self.start_warnings.clone()
        }

        /// Stop capture and drain the worker. `AudioDeviceStop` waits for
        /// the current IO cycle; callers still run this in `spawn_blocking`
        /// raced against a timeout, never under a lock another command needs.
        pub fn stop_capture_blocking(&self) -> Result<(), AppError> {
            let taken = self.resources.lock().expect("resources poisoned").take();
            let result = match taken {
                Some(mut res) => res.teardown(),
                None => Ok(()),
            };
            if let Some(worker) = self.worker.lock().expect("worker poisoned").take() {
                if worker.join().is_err() {
                    log::error!("audio worker thread panicked");
                }
            }
            result
        }

        /// Finalize the WAV header. Idempotent; safe whether or not stop
        /// succeeded — a partial recording is still a playable WAV.
        pub fn finalize_writer(&self) -> Result<(), AppError> {
            if let Some(w) = self.writer.lock().expect("writer poisoned").take() {
                w.finalize()
                    .map_err(|e| AppError::Io(format!("finalize wav: {e}")))?;
            }
            Ok(())
        }

        /// Hard failures for recordings that captured nothing, plus
        /// warnings for suspicious-but-usable ones.
        pub fn diagnose(&self) -> Result<Vec<String>, AppError> {
            let cb = self.stats.callbacks.load(Ordering::Relaxed);
            let sw = self.stats.samples_written.load(Ordering::Relaxed);
            let dropped = self.stats.dropped_buffers.load(Ordering::Relaxed);
            let bytes = std::fs::metadata(&self.path).map(|m| m.len()).unwrap_or(0);
            let (system_peak, mic_peak) = (self.stats.system_peak(), self.stats.mic_peak());
            log::info!(
                "stopped capture: callbacks={cb} samples_written={sw} dropped={dropped} \
                 system_peak={system_peak:.4} mic_peak={mic_peak:.4} wav_bytes={bytes} path={}",
                self.path.display()
            );
            if cb == 0 {
                return Err(AppError::Backend(
                    "Core Audio delivered zero callbacks — the capture device never ran. \
                     Check System Settings > Privacy & Security (Microphone and System Audio \
                     Recording), or reset with `tccutil reset AudioCapture click.atomai.ttobak.mac` \
                     and relaunch."
                        .into(),
                ));
            }
            if sw == 0 {
                return Err(AppError::Backend(format!(
                    "Core Audio delivered {cb} callbacks but no samples were written — the \
                     buffer layout did not match the expected tap/microphone streams."
                )));
            }
            let mut warnings = Vec::new();
            if system_peak == 0.0 {
                warnings.push(
                    "System audio was silent for the whole recording — System Audio Recording \
                     permission may be denied, or nothing was playing."
                        .to_string(),
                );
            }
            if self.mic_active && mic_peak == 0.0 {
                warnings.push(
                    "The microphone was silent for the whole recording — Microphone permission \
                     may be denied or the input is muted."
                        .to_string(),
                );
            }
            if dropped > 0 {
                warnings.push(format!(
                    "{dropped} audio buffers were dropped because the disk writer fell behind."
                ));
            }
            Ok(warnings)
        }

        /// Stop, THEN finalize regardless of the stop result, THEN diagnose.
        /// Error precedence: finalize > stop > diagnose (a real stop/finalize
        /// error must never be masked by a passing diagnose).
        pub fn stop_and_finalize(&self) -> Result<Vec<String>, AppError> {
            let stop_result = self.stop_capture_blocking();
            let finalize_result = self.finalize_writer();
            if let Err(e) = &finalize_result {
                log::error!("finalize_writer failed: {e}");
            }
            if let Err(e) = &stop_result {
                log::error!("stop capture failed (finalize still ran best-effort): {e}");
            }
            let diagnose_result = self.diagnose();
            if let Err(e) = &diagnose_result {
                log::warn!("diagnose reported: {e}");
            }
            match (finalize_result, stop_result) {
                (Err(fin_err), Err(stop_err)) => Err(AppError::Backend(format!(
                    "finalize_writer failed: {fin_err} (stop capture also failed: {stop_err})"
                ))),
                (Err(fin_err), Ok(())) => Err(fin_err),
                (Ok(()), Err(stop_err)) => Err(stop_err),
                (Ok(()), Ok(())) => diagnose_result,
            }
        }
    }

    impl Drop for Backend {
        fn drop(&mut self) {
            // Normal paths already stopped; this only covers a Backend
            // dropped without stop_and_finalize (e.g. a panic).
            let _ = self.stop_capture_blocking();
            let _ = self.finalize_writer();
        }
    }

    /// Real-time IOProc: mix and enqueue only. Never blocks, never logs
    /// beyond the one-time layout line, and never unwinds into Core Audio.
    unsafe extern "C-unwind" fn io_proc(
        _device: AudioObjectID,
        _now: NonNull<AudioTimeStamp>,
        input: NonNull<AudioBufferList>,
        _input_time: NonNull<AudioTimeStamp>,
        _output: NonNull<AudioBufferList>,
        _output_time: NonNull<AudioTimeStamp>,
        client: *mut c_void,
    ) -> i32 {
        let _ = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            if client.is_null() {
                return;
            }
            let ctx = &*(client as *const CallbackCtx);
            if ctx.current_generation.load(Ordering::Relaxed) != ctx.my_generation {
                return;
            }
            ctx.stats.callbacks.fetch_add(1, Ordering::Relaxed);

            let list = input.as_ref();
            let buffers: &[AudioBuffer] =
                std::slice::from_raw_parts(list.mBuffers.as_ptr(), list.mNumberBuffers as usize);
            let views: Vec<BufferView<'_>> = buffers
                .iter()
                .map(|b| {
                    let channels = b.mNumberChannels as usize;
                    let data: &[f32] = if b.mData.is_null() || channels == 0 {
                        &[]
                    } else {
                        std::slice::from_raw_parts(
                            b.mData as *const f32,
                            b.mDataByteSize as usize / 4,
                        )
                    };
                    BufferView { channels, data }
                })
                .collect();

            if ctx
                .stats
                .logged_first
                .compare_exchange(0, 1, Ordering::Relaxed, Ordering::Relaxed)
                .is_ok()
            {
                let shape: Vec<(usize, usize)> =
                    views.iter().map(|v| (v.channels, v.data.len())).collect();
                log::info!(
                    "first IOProc buffer list (channels, samples): {shape:?} layout={:?}",
                    ctx.layout
                );
            }

            let mixed = mix::mix(&views, ctx.layout, mix::MIC_GAIN);
            ctx.stats
                .system_peak_bits
                .fetch_max(mixed.system_peak.to_bits(), Ordering::Relaxed);
            ctx.stats
                .mic_peak_bits
                .fetch_max(mixed.mic_peak.to_bits(), Ordering::Relaxed);
            if mixed.stereo.is_empty() {
                return;
            }
            // try_lock: only teardown ever contends, and it runs after stop.
            if let Ok(guard) = ctx.tx.try_lock() {
                if let Some(tx) = guard.as_ref() {
                    if let Err(TrySendError::Full(_)) = tx.try_send(mixed.stereo) {
                        ctx.stats.dropped_buffers.fetch_add(1, Ordering::Relaxed);
                    }
                }
            }
        }));
        0
    }

    fn spawn_worker(
        rx: Receiver<Vec<f32>>,
        writer: Arc<Mutex<Option<Wav>>>,
        stats: Arc<Stats>,
        app: AppHandle,
        sample_rate: u32,
        my_generation: u64,
        current_generation: Arc<AtomicU64>,
    ) -> Result<JoinHandle<()>, AppError> {
        std::thread::Builder::new()
            .name("ttobak-audio-writer".into())
            .spawn(move || {
                let flush_every = sample_rate as u64 * CHANNELS as u64 * FLUSH_INTERVAL_SECONDS;
                let mut since_flush = 0u64;
                let mut last_emit_ms = 0u64;
                let mut resampler = Resampler::new(sample_rate as f64, PCM_TARGET_SAMPLE_RATE);
                let mut pcm_pending: Vec<f32> = Vec::with_capacity(PCM_CHUNK_SAMPLES * 2);
                // Ends when teardown drops the sender after AudioDeviceStop.
                while let Ok(stereo) = rx.recv() {
                    let mut written = 0u64;
                    if let Ok(mut guard) = writer.lock() {
                        if let Some(w) = guard.as_mut() {
                            for &s in &stereo {
                                let v = (s.clamp(-1.0, 1.0) * i16::MAX as f32) as i16;
                                if let Err(e) = w.write_sample(v) {
                                    log::warn!("wav write error: {e}");
                                    break;
                                }
                                written += 1;
                            }
                            since_flush += written;
                            if since_flush >= flush_every {
                                match w.flush() {
                                    Ok(()) => since_flush = 0,
                                    Err(e) => {
                                        log::warn!("periodic wav flush failed (will retry): {e}")
                                    }
                                }
                            }
                        }
                    }
                    stats.samples_written.fetch_add(written, Ordering::Relaxed);

                    // Superseded (stopped, maybe a newer recording started):
                    // keep draining into this WAV, but never emit — events
                    // are global and would leak into the newer recording.
                    if current_generation.load(Ordering::Relaxed) != my_generation {
                        continue;
                    }

                    let now = now_ms();
                    if now.saturating_sub(last_emit_ms) >= LEVEL_EMIT_INTERVAL_MS {
                        last_emit_ms = now;
                        let sum_sq: f32 = stereo.iter().map(|s| s * s).sum();
                        let rms = (sum_sq / stereo.len().max(1) as f32).sqrt();
                        let _ = app.emit("native-audio-level", (rms / 0.25).min(1.0));
                    }

                    pcm_pending.extend(resampler.process(&mix::downmix_stereo(&stereo)));
                    while pcm_pending.len() >= PCM_CHUNK_SAMPLES {
                        let mut bytes = Vec::with_capacity(PCM_CHUNK_SAMPLES * 2);
                        for s in pcm_pending.drain(..PCM_CHUNK_SAMPLES) {
                            let c = s.clamp(-1.0, 1.0);
                            let v = if c < 0.0 {
                                (c * 0x8000 as f32) as i16
                            } else {
                                (c * 0x7FFF as f32) as i16
                            };
                            bytes.extend_from_slice(&v.to_le_bytes());
                        }
                        let encoded = base64::engine::general_purpose::STANDARD.encode(&bytes);
                        let _ = app.emit("native-pcm-chunk", encoded);
                    }
                }
            })
            .map_err(|e| AppError::Backend(format!("spawn audio writer: {e}")))
    }

    // --- Core Audio property helpers ------------------------------------

    fn check(what: &str, status: i32) -> Result<(), AppError> {
        if status == 0 {
            Ok(())
        } else {
            Err(AppError::Backend(format!(
                "{what} failed: OSStatus {status}"
            )))
        }
    }

    fn address(
        selector: AudioObjectPropertySelector,
        scope: AudioObjectPropertyScope,
    ) -> AudioObjectPropertyAddress {
        AudioObjectPropertyAddress {
            mSelector: selector,
            mScope: scope,
            mElement: kAudioObjectPropertyElementMain,
        }
    }

    /// Reads a fixed-size property value.
    fn get_property<T: Copy>(
        object: AudioObjectID,
        selector: AudioObjectPropertySelector,
        scope: AudioObjectPropertyScope,
        qualifier: Option<&[u8]>,
    ) -> Option<T> {
        let addr = address(selector, scope);
        let mut value = std::mem::MaybeUninit::<T>::uninit();
        let mut size = std::mem::size_of::<T>() as u32;
        let (q_size, q_ptr) = qualifier.map_or((0, std::ptr::null()), |q| {
            (q.len() as u32, q.as_ptr().cast())
        });
        let status = unsafe {
            AudioObjectGetPropertyData(
                object,
                NonNull::from(&addr),
                q_size,
                q_ptr,
                NonNull::from(&mut size),
                NonNull::new(value.as_mut_ptr().cast::<c_void>())?,
            )
        };
        (status == 0 && size as usize == std::mem::size_of::<T>())
            .then(|| unsafe { value.assume_init() })
    }

    fn get_f64(
        object: AudioObjectID,
        selector: AudioObjectPropertySelector,
        scope: AudioObjectPropertyScope,
    ) -> Option<f64> {
        get_property::<f64>(object, selector, scope, None)
    }

    fn default_device(selector: AudioObjectPropertySelector) -> Option<AudioObjectID> {
        get_property::<AudioObjectID>(
            kAudioObjectSystemObject as AudioObjectID,
            selector,
            kAudioObjectPropertyScopeGlobal,
            None,
        )
        .filter(|id| *id != 0)
    }

    fn own_process_object() -> Option<AudioObjectID> {
        let pid = std::process::id() as i32;
        get_property::<AudioObjectID>(
            kAudioObjectSystemObject as AudioObjectID,
            kAudioHardwarePropertyTranslatePIDToProcessObject,
            kAudioObjectPropertyScopeGlobal,
            Some(&pid.to_ne_bytes()),
        )
        .filter(|id| *id != 0)
    }

    fn device_uid(device: AudioObjectID) -> Option<String> {
        let raw = get_property::<*const c_void>(
            device,
            kAudioDevicePropertyDeviceUID,
            kAudioObjectPropertyScopeGlobal,
            None,
        )?;
        if raw.is_null() {
            return None;
        }
        // The HAL returns a +1 retained CFString.
        let uid = unsafe {
            CFString::wrap_under_create_rule(raw as core_foundation::string::CFStringRef)
        };
        Some(uid.to_string())
    }

    /// Total input channels of a device (sum over its input stream buffers).
    fn input_channels(device: AudioObjectID) -> usize {
        let addr = address(
            kAudioDevicePropertyStreamConfiguration,
            kAudioObjectPropertyScopeInput,
        );
        let mut size = 0u32;
        let status = unsafe {
            AudioObjectGetPropertyDataSize(
                device,
                NonNull::from(&addr),
                0,
                std::ptr::null(),
                NonNull::from(&mut size),
            )
        };
        if status != 0 || (size as usize) < std::mem::size_of::<AudioBufferList>() {
            return 0;
        }
        // u64 storage keeps the AudioBufferList suitably aligned.
        let mut storage = vec![0u64; (size as usize).div_ceil(8)];
        let status = unsafe {
            AudioObjectGetPropertyData(
                device,
                NonNull::from(&addr),
                0,
                std::ptr::null(),
                NonNull::from(&mut size),
                NonNull::new(storage.as_mut_ptr().cast::<c_void>()).expect("vec pointer"),
            )
        };
        if status != 0 {
            return 0;
        }
        unsafe {
            let list = &*(storage.as_ptr() as *const AudioBufferList);
            let count = list.mNumberBuffers as usize;
            // Never read past the bytes the HAL actually wrote.
            let header = std::mem::offset_of!(AudioBufferList, mBuffers);
            let max = (size as usize).saturating_sub(header) / std::mem::size_of::<AudioBuffer>();
            std::slice::from_raw_parts(list.mBuffers.as_ptr(), count.min(max))
                .iter()
                .map(|b| b.mNumberChannels as usize)
                .sum()
        }
    }

    fn cf_dict(pairs: &[(&CStr, CFType)]) -> CFType {
        let pairs: Vec<(CFString, CFType)> = pairs
            .iter()
            .map(|(k, v)| {
                (
                    CFString::new(k.to_str().expect("ASCII Core Audio key")),
                    v.clone(),
                )
            })
            .collect();
        CFDictionary::from_CFType_pairs(&pairs).as_CFType()
    }

    fn now_ms() -> u64 {
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_millis() as u64)
            .unwrap_or(0)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // --- StartGuard -------------------------------------------------------

    #[test]
    fn dropped_guard_releases_reservation() {
        let recorder = Mutex::new(AudioRecorder::new());
        recorder.lock().begin_start().unwrap();
        {
            let _guard = StartGuard::new(&recorder);
            assert!(
                matches!(recorder.lock().begin_start(), Err(AppError::AlreadyRunning)),
                "reservation must be held while the guard is alive"
            );
        }
        // Guard dropped without disarm (the failure / cancelled-future path).
        assert!(
            recorder.lock().begin_start().is_ok(),
            "drop must cancel the reservation"
        );
    }

    #[test]
    fn disarmed_guard_keeps_reservation() {
        let recorder = Mutex::new(AudioRecorder::new());
        recorder.lock().begin_start().unwrap();
        let guard = StartGuard::new(&recorder);
        guard.disarm();
        assert!(
            matches!(recorder.lock().begin_start(), Err(AppError::AlreadyRunning)),
            "disarm must leave the reservation in place (install() owns it now)"
        );
    }

    #[test]
    fn guard_releases_on_early_return() {
        fn fallible(recorder: &Mutex<AudioRecorder>, fail: bool) -> Result<(), AppError> {
            recorder.lock().begin_start()?;
            let guard = StartGuard::new(recorder);
            if fail {
                return Err(AppError::Io("simulated backend failure".into()));
            }
            guard.disarm();
            Ok(())
        }
        let recorder = Mutex::new(AudioRecorder::new());
        assert!(fallible(&recorder, true).is_err());
        assert!(
            recorder.lock().begin_start().is_ok(),
            "early `return Err` must release via Drop"
        );
        recorder.lock().cancel_start();
        assert!(fallible(&recorder, false).is_ok());
        assert!(matches!(
            recorder.lock().begin_start(),
            Err(AppError::AlreadyRunning)
        ));
    }
}
