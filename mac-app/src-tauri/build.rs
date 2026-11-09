/// App commands the remote SPA may invoke. Tauri 2 checks the ACL for every
/// command called from a remote origin, so each one needs an `allow-*`
/// permission granted in `capabilities/default.json`; a command missing here
/// or there is rejected with "not allowed by ACL".
const COMMANDS: &[&str] = &[
    "start_recording",
    "stop_recording",
    "list_input_devices",
    "switch_recording_input",
    "recording_status",
    "upload_recording",
    "cleanup_recording",
    "release_recording_power",
    "list_leftover_recordings",
    "control_ready",
    "control_reply",
    "control_report_state",
    "meeting_detection_get",
    "meeting_detection_set_enabled",
    "meeting_detection_respond",
    "meeting_prompt_choose",
];

fn main() {
    tauri_build::try_build(
        tauri_build::Attributes::new()
            .app_manifest(tauri_build::AppManifest::new().commands(COMMANDS)),
    )
    .expect("failed to run tauri-build");
}
