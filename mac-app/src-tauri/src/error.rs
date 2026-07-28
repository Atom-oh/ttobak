use serde::Serialize;
use thiserror::Error;

#[derive(Debug, Error)]
pub enum AppError {
    #[error("audio capture not supported on this platform")]
    Unsupported,

    #[error("recorder is already running")]
    AlreadyRunning,

    #[error("recorder is not running")]
    NotRunning,

    #[error("permission denied: {0}")]
    Permission(String),

    #[error("io: {0}")]
    Io(String),

    #[error("audio backend: {0}")]
    Backend(String),
}

// Tauri commands need errors to be serializable.
impl Serialize for AppError {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: serde::Serializer,
    {
        serializer.serialize_str(&self.to_string())
    }
}
