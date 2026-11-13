//! Finish a recording when the Mac goes to sleep (e.g. the lid closes
//! without "stop"). On IOKit's system-will-sleep notice the app stops the
//! running recording through the normal control path, with the notes step
//! skipped so the upload starts, and delays sleep until that stop has been
//! answered or `STOP_DEADLINE` passes (macOS allows about 30 s). The upload
//! itself is resumed after wake: the app emits `native-system-wake` and the
//! record page retries a pending upload. A closed lid that does not sleep
//! (clamshell mode on an external display) keeps recording.

#[cfg(target_os = "macos")]
pub use mac::start;

/// Sleep is delayed at most this long while the recording stops.
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
const STOP_DEADLINE: std::time::Duration = std::time::Duration::from_secs(20);

#[cfg(target_os = "macos")]
mod mac {
    use std::ffi::c_void;
    use std::sync::atomic::{AtomicU32, Ordering};
    use std::sync::OnceLock;

    use core_foundation::base::TCFType;
    use core_foundation::runloop::{kCFRunLoopDefaultMode, CFRunLoop, CFRunLoopSourceRef};
    use tauri::{AppHandle, Emitter, Manager};

    use super::STOP_DEADLINE;
    use crate::control_proto::Action;
    use crate::RecorderState;

    type IoObject = u32;
    type IoConnect = u32;
    type NotificationPort = *mut c_void;

    const CAN_SYSTEM_SLEEP: u32 = 0xe000_0270;
    const SYSTEM_WILL_SLEEP: u32 = 0xe000_0280;
    const SYSTEM_HAS_POWERED_ON: u32 = 0xe000_0300;

    #[link(name = "IOKit", kind = "framework")]
    extern "C" {
        fn IORegisterForSystemPower(
            refcon: *mut c_void,
            port: *mut NotificationPort,
            callback: extern "C" fn(*mut c_void, IoObject, u32, *mut c_void),
            notifier: *mut IoObject,
        ) -> IoConnect;
        fn IONotificationPortGetRunLoopSource(port: NotificationPort) -> CFRunLoopSourceRef;
        fn IOAllowPowerChange(kernel_port: IoConnect, notification_id: isize) -> i32;
    }

    static ROOT_PORT: AtomicU32 = AtomicU32::new(0);
    static APP: OnceLock<AppHandle> = OnceLock::new();

    /// Registers for system sleep/wake notices on a dedicated run loop.
    pub fn start(app: AppHandle) {
        if APP.set(app).is_err() {
            return;
        }
        let spawned = std::thread::Builder::new()
            .name("system-sleep".into())
            .spawn(|| unsafe {
                let mut port: NotificationPort = std::ptr::null_mut();
                let mut notifier: IoObject = 0;
                let root = IORegisterForSystemPower(
                    std::ptr::null_mut(),
                    &mut port,
                    on_power,
                    &mut notifier,
                );
                if root == 0 || port.is_null() {
                    log::warn!("could not register for system sleep notices");
                    return;
                }
                ROOT_PORT.store(root, Ordering::Relaxed);
                let source = IONotificationPortGetRunLoopSource(port);
                let run_loop = CFRunLoop::get_current();
                let source = core_foundation::runloop::CFRunLoopSource::wrap_under_get_rule(source);
                run_loop.add_source(&source, kCFRunLoopDefaultMode);
                CFRunLoop::run_current();
            });
        if let Err(e) = spawned {
            log::warn!("could not start the system sleep watcher: {e}");
        }
    }

    extern "C" fn on_power(_: *mut c_void, _: IoObject, message: u32, argument: *mut c_void) {
        let id = argument as isize;
        let allow = move || unsafe {
            IOAllowPowerChange(ROOT_PORT.load(Ordering::Relaxed), id);
        };
        let Some(app) = APP.get() else {
            if message == CAN_SYSTEM_SLEEP || message == SYSTEM_WILL_SLEEP {
                allow();
            }
            return;
        };
        match message {
            CAN_SYSTEM_SLEEP => allow(),
            SYSTEM_WILL_SLEEP => {
                let snapshot = app.state::<RecorderState>().recorder.lock().snapshot();
                if snapshot.listening {
                    // Live interpretation has nothing to save: stop the
                    // capture, tell the page why, then let the system sleep.
                    log::info!("system will sleep while listening: stopping");
                    let app = app.clone();
                    tauri::async_runtime::spawn(async move {
                        let stop = crate::end_listening(&app, Some("system-sleep"));
                        if tokio::time::timeout(STOP_DEADLINE, stop).await.is_err() {
                            log::warn!("listening stop not finished before the deadline");
                        }
                        allow();
                    });
                    return;
                }
                if !snapshot.recording {
                    allow();
                    return;
                }
                log::info!("system will sleep while recording: stopping and uploading");
                let app = app.clone();
                tauri::async_runtime::spawn(async move {
                    let stop = crate::control::dispatch_local(
                        &app,
                        "system-sleep",
                        Action::Stop { upload: true },
                    );
                    match tokio::time::timeout(STOP_DEADLINE, stop).await {
                        Ok(reply) => log::info!("sleep stop answered: ok={}", reply["ok"]),
                        Err(_) => log::warn!("sleep stop not answered before the deadline"),
                    }
                    allow();
                });
            }
            SYSTEM_HAS_POWERED_ON => {
                let _ = app.emit("native-system-wake", ());
            }
            _ => {}
        }
    }
}
