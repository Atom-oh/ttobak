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
    "update_prompt_choose",
    "caption_settings_get",
    "caption_settings_set",
    "caption_translate",
    "caption_overlay_open",
    "caption_overlay_close",
    "caption_overlay_set_style",
    "caption_overlay_push",
    "intake_get_state",
    "intake_set_schedule",
    "intake_set_node_path",
    "intake_launch_agent_off",
    "intake_run_now",
    "intake_setup",
    "intake_login",
    "intake_cancel",
    "intake_check_adapter",
    "intake_update_adapter",
    "intake_refresh_path",
    "intake_set_options",
    "report_generate",
    "report_cancel",
    "report_get_state",
    "report_settings_get",
    "report_settings_set",
    "report_slack_channels_refresh",
    // Granted only to the local overlay windows (capabilities/caption-overlay.json).
    "caption_overlay_idle",
];

fn main() {
    tauri_build::try_build(
        tauri_build::Attributes::new()
            .app_manifest(tauri_build::AppManifest::new().commands(COMMANDS)),
    )
    .expect("failed to run tauri-build");
}
