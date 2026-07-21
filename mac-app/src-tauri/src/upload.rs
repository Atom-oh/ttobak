//! Streaming upload of a recorded WAV straight from disk to a presigned S3
//! URL.
//!
//! This exists to replace `read_recording_bytes` (removed from `lib.rs`),
//! which used to read an entire recording into memory and ship it through
//! Tauri's IPC bridge to the WebView. On a real ~35-minute recording
//! (400,949,804 bytes) that crashed JavaScriptCore — because this app's
//! WebView loads the remote production SPA rather than a local `tauri://`
//! origin, Tauri delivers IPC binary responses via `evaluateJavaScript` as
//! one giant JS array literal, and the bytecode compiler fatally asserted on
//! it. See `docs/decisions/ADR-024-native-streaming-upload-and-system-audio-captions.md`.
//!
//! The fix: bulk audio bytes never cross the IPC bridge again. Presign,
//! auth, and completion-notification still live in the SPA (unchanged from
//! ADR-006's original "upload lives in the SPA" boundary) — only the byte
//! *transport* moves here.

use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Duration;

use futures_util::TryStreamExt;
use serde::Serialize;
use tauri::{AppHandle, Emitter, State};
use tokio_util::io::ReaderStream;

use crate::error::AppError;
use crate::{validate_recording_path, RecorderState};

/// Abort the upload if no new bytes have been sent for this long. There is
/// deliberately no *total* request timeout — a large file on a slow but
/// healthy connection should be allowed to keep going; only a truly stalled
/// connection (dead TCP, wedged proxy) should be aborted. This is the
/// replacement for the frontend's old `uploadAudioWithRetry`, which aborted
/// every attempt at a fixed 30 seconds regardless of file size or progress.
const STALL_TIMEOUT: Duration = Duration::from_secs(60);

/// How often to sample bytes-sent and emit a progress event.
const PROGRESS_INTERVAL: Duration = Duration::from_millis(300);

/// Read buffer size for streaming the file off disk.
const READ_CHUNK_SIZE: usize = 64 * 1024;

#[derive(Clone, Serialize)]
struct UploadProgress {
    loaded: u64,
    total: u64,
}

/// The only S3 bucket this app is ever allowed to upload a recording to.
/// The account ID and bucket-naming convention are already hardcoded
/// elsewhere in this repo's infra (`infra/lib/storage-stack.ts`'s
/// `ttobak-assets-${cdk.Aws.ACCOUNT_ID}`, region `ap-northeast-2` per
/// `infra/bin/infra.ts`'s default) — duplicating the concrete value here,
/// rather than accepting any `*.amazonaws.com` suffix, is deliberate: a
/// bare suffix check accepts ANY AWS customer's bucket (bucket names are
/// globally attacker-choosable), which would defeat the point of this
/// check entirely under a compromised-frontend precondition (this app's
/// WebView fully controls the `upload_url` argument it passes in).
const EXPECTED_BUCKET_HOST: &str = "ttobak-assets-180294183052.s3.ap-northeast-2.amazonaws.com";

/// Parse and validate a presigned upload URL: must be `https` and hosted on
/// exactly [`EXPECTED_BUCKET_HOST`]. Split out from the command itself so
/// it can be unit tested without any network or Tauri context.
pub(crate) fn validate_upload_url(upload_url: &str) -> Result<reqwest::Url, AppError> {
    let parsed = reqwest::Url::parse(upload_url)
        .map_err(|e| AppError::Backend(format!("invalid upload_url: {e}")))?;
    if parsed.scheme() != "https" {
        return Err(AppError::Backend(format!(
            "upload_url must be https, got scheme {:?}",
            parsed.scheme()
        )));
    }
    let host = parsed.host_str().unwrap_or("");
    if host != EXPECTED_BUCKET_HOST {
        return Err(AppError::Backend(format!(
            "upload_url host {host:?} is not the expected TTOBAK audio bucket \
             ({EXPECTED_BUCKET_HOST}) — refusing to upload"
        )));
    }
    Ok(parsed)
}

/// Stream `file_path` to `url` via HTTP PUT, calling `on_progress(loaded,
/// total)` roughly every `PROGRESS_INTERVAL`. Returns the response status
/// code on success (2xx); any other status, or a stalled transfer, is an
/// `Err`. Pure of any Tauri types so it can run under a plain `tokio::test`
/// against a local mock server — the security-relevant URL validation
/// happens in `validate_upload_url`, called by the command before this.
async fn stream_file_to_url(
    file_path: &Path,
    url: reqwest::Url,
    content_type: &str,
    mut on_progress: impl FnMut(u64, u64),
) -> Result<u16, AppError> {
    let file = tokio::fs::File::open(file_path)
        .await
        .map_err(|e| AppError::Io(format!("open {}: {e}", file_path.display())))?;
    let total = file
        .metadata()
        .await
        .map_err(|e| AppError::Io(format!("stat {}: {e}", file_path.display())))?
        .len();

    let sent = Arc::new(AtomicU64::new(0));
    let sent_for_stream = Arc::clone(&sent);
    let stream = ReaderStream::with_capacity(file, READ_CHUNK_SIZE).inspect_ok(move |chunk| {
        sent_for_stream.fetch_add(chunk.len() as u64, Ordering::Relaxed);
    });
    let body = reqwest::Body::wrap_stream(stream);

    let client = reqwest::Client::builder()
        .connect_timeout(Duration::from_secs(15))
        .build()
        .map_err(|e| AppError::Backend(format!("build http client: {e}")))?;

    // Explicit Content-Length is the single most load-bearing detail here:
    // without it, a streamed body with an unknown size hint goes out with
    // chunked Transfer-Encoding, which S3 presigned PUT rejects outright
    // (501 Not Implemented). Setting it manually alongside a streaming body
    // is the same pattern reqwest's own `multipart()` helper uses internally
    // when it can compute the length up front. Verified against a local mock
    // server in this module's tests, not just reasoned about.
    let request = client
        .put(url)
        .header(reqwest::header::CONTENT_LENGTH, total)
        .header(reqwest::header::CONTENT_TYPE, content_type)
        .body(body)
        .send();

    let progress_watchdog = async {
        let mut last_seen = 0u64;
        let mut stalled_since = tokio::time::Instant::now();
        loop {
            tokio::time::sleep(PROGRESS_INTERVAL).await;
            let loaded = sent.load(Ordering::Relaxed);
            on_progress(loaded, total);
            if loaded != last_seen {
                last_seen = loaded;
                stalled_since = tokio::time::Instant::now();
            } else if stalled_since.elapsed() >= STALL_TIMEOUT {
                // Returning here makes `select!` below pick this arm, which
                // drops (and thereby cancels) the still-pending `request`
                // future — this is the actual abort mechanism, there is no
                // separate cancellation handle to manage.
                return;
            }
        }
    };

    let response = tokio::select! {
        result = request => result.map_err(|e| AppError::Backend(format!("upload request failed: {e}")))?,
        _ = progress_watchdog => {
            let loaded = sent.load(Ordering::Relaxed);
            return Err(AppError::Backend(format!(
                "upload stalled — no progress for {}s ({loaded} / {total} bytes sent)",
                STALL_TIMEOUT.as_secs()
            )));
        }
    };

    let status = response.status();
    if !status.is_success() {
        let snippet: String = response
            .text()
            .await
            .unwrap_or_default()
            .chars()
            .take(256)
            .collect();
        return Err(AppError::Backend(format!(
            "upload failed: HTTP {status} — {snippet}"
        )));
    }

    Ok(status.as_u16())
}

/// Stream `path` (must be a file this recording session created — validated
/// via the same whitelist `cleanup_recording` uses) to `upload_url`. Never
/// deletes the source file — the caller (frontend) is responsible for
/// calling `cleanup_recording` only after the SPA's own `notifyComplete`
/// call succeeds, so a recording is never lost to a failed upload.
#[tauri::command]
pub async fn upload_recording(
    path: String,
    upload_url: String,
    content_type: String,
    state: State<'_, RecorderState>,
    app: AppHandle,
) -> Result<u16, AppError> {
    let canonical = {
        let recorded = state.recorded_paths.lock();
        validate_recording_path(&path, &recorded)?
    };
    let url = validate_upload_url(&upload_url)?;

    stream_file_to_url(&canonical, url, &content_type, move |loaded, total| {
        let _ = app.emit("native-upload-progress", UploadProgress { loaded, total });
    })
    .await
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::net::TcpListener;

    /// Raw TCP mock: accepts one connection, reads the request line +
    /// headers, and (if a `Content-Length` header is present) reads exactly
    /// that many body bytes. Returns the parsed headers (lowercased names)
    /// and the body it received, so tests can assert on the *actual wire
    /// framing* reqwest chose — not just that the call "succeeded" — which
    /// is the whole point of this test (S3 presigned PUT rejects chunked
    /// Transfer-Encoding with a 501; we need proof reqwest doesn't send it
    /// when an explicit Content-Length is set on a streaming body).
    struct RecordedRequest {
        headers: std::collections::HashMap<String, String>,
        body: Vec<u8>,
    }

    fn spawn_mock_server(
        response_status_line: &'static str,
        response_body: &'static [u8],
    ) -> (u16, std::sync::mpsc::Receiver<RecordedRequest>) {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind mock server");
        let port = listener.local_addr().expect("local_addr").port();
        let (tx, rx) = std::sync::mpsc::channel();

        std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().expect("accept");

            // Read headers (until CRLFCRLF), then the body if Content-Length
            // is present. This is deliberately minimal — just enough to
            // inspect framing, not a general HTTP parser.
            let mut buf = Vec::new();
            let mut chunk = [0u8; 4096];
            let headers_end = loop {
                let n = stream.read(&mut chunk).expect("read headers");
                assert!(n > 0, "connection closed before headers completed");
                buf.extend_from_slice(&chunk[..n]);
                if let Some(pos) = find_double_crlf(&buf) {
                    break pos;
                }
            };
            let header_text = String::from_utf8_lossy(&buf[..headers_end]).to_string();
            let mut headers = std::collections::HashMap::new();
            for line in header_text.lines().skip(1) {
                if let Some((k, v)) = line.split_once(':') {
                    headers.insert(k.trim().to_lowercase(), v.trim().to_string());
                }
            }

            let mut body = buf[headers_end + 4..].to_vec();
            if let Some(len_str) = headers.get("content-length") {
                let expected_len: usize = len_str.parse().expect("valid content-length");
                while body.len() < expected_len {
                    let n = stream.read(&mut chunk).expect("read body");
                    assert!(n > 0, "connection closed before body completed");
                    body.extend_from_slice(&chunk[..n]);
                }
            }

            let response = format!(
                "{response_status_line}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                response_body.len()
            );
            stream.write_all(response.as_bytes()).expect("write status line");
            stream.write_all(response_body).expect("write response body");

            let _ = tx.send(RecordedRequest { headers, body });
        });

        (port, rx)
    }

    fn find_double_crlf(buf: &[u8]) -> Option<usize> {
        buf.windows(4).position(|w| w == b"\r\n\r\n")
    }

    async fn write_temp_file(name: &str, contents: &[u8]) -> std::path::PathBuf {
        let mut path = std::env::temp_dir();
        path.push(format!("ttobak-mac-upload-test-{name}-{}", std::process::id()));
        tokio::fs::write(&path, contents).await.expect("write temp file");
        path
    }

    #[tokio::test]
    async fn streams_file_with_explicit_content_length_and_no_chunked_encoding() {
        // ~200KB — large enough that reqwest/hyper would default to a
        // streamed, unknown-length (chunked) body without our explicit
        // Content-Length header, and large enough to span several
        // `READ_CHUNK_SIZE` (64KB) reads.
        let contents: Vec<u8> = (0..200_000u32).map(|i| (i % 256) as u8).collect();
        let file_path = write_temp_file("basic", &contents).await;

        let (port, rx) = spawn_mock_server("HTTP/1.1 200 OK", b"");
        let url = reqwest::Url::parse(&format!("http://127.0.0.1:{port}/put-target"))
            .expect("valid mock url");

        let mut progress_calls = Vec::new();
        let status = stream_file_to_url(&file_path, url, "audio/wav", |loaded, total| {
            progress_calls.push((loaded, total));
        })
        .await
        .expect("upload should succeed");

        assert_eq!(status, 200);

        let recorded = rx
            .recv_timeout(Duration::from_secs(5))
            .expect("mock server should have recorded a request");

        assert_eq!(
            recorded.headers.get("content-length").map(String::as_str),
            Some(contents.len().to_string().as_str()),
            "Content-Length must equal the file size"
        );
        assert!(
            !recorded.headers.contains_key("transfer-encoding"),
            "must not fall back to chunked Transfer-Encoding — S3 presigned PUT rejects it \
             with a 501; headers were: {:?}",
            recorded.headers
        );
        assert_eq!(
            recorded.body, contents,
            "the mock server must receive the exact same bytes that were on disk"
        );

        tokio::fs::remove_file(&file_path).await.ok();
    }

    #[tokio::test]
    async fn non_success_status_is_reported_as_error_with_body_snippet() {
        let contents = b"small file".to_vec();
        let file_path = write_temp_file("error-status", &contents).await;

        let (port, _rx) = spawn_mock_server(
            "HTTP/1.1 403 Forbidden",
            b"<Error><Code>AccessDenied</Code></Error>",
        );
        let url = reqwest::Url::parse(&format!("http://127.0.0.1:{port}/put-target"))
            .expect("valid mock url");

        let err = stream_file_to_url(&file_path, url, "audio/wav", |_, _| {})
            .await
            .expect_err("a 403 response must be surfaced as an error, not swallowed");

        let message = err.to_string();
        assert!(message.contains("403"), "error should mention the status code: {message}");
        assert!(
            message.contains("AccessDenied"),
            "error should include a snippet of the response body: {message}"
        );

        tokio::fs::remove_file(&file_path).await.ok();
    }

    #[test]
    fn validate_upload_url_accepts_the_expected_bucket_host() {
        let result = validate_upload_url(&format!(
            "https://{EXPECTED_BUCKET_HOST}/audio/foo.wav?X-Amz-Signature=abc",
        ));
        assert!(result.is_ok(), "expected a valid S3 presigned URL to pass: {result:?}");
    }

    #[test]
    fn validate_upload_url_rejects_non_https_scheme() {
        let result = validate_upload_url(&format!("http://{EXPECTED_BUCKET_HOST}/audio/foo.wav"));
        assert!(result.is_err(), "plain http must be rejected");
    }

    #[test]
    fn validate_upload_url_rejects_non_amazonaws_host() {
        let result = validate_upload_url("https://evil.example.com/audio/foo.wav");
        assert!(result.is_err(), "a non-amazonaws.com host must be rejected");
    }

    #[test]
    fn validate_upload_url_rejects_a_different_amazonaws_bucket() {
        // Regression test: a bare `.amazonaws.com` suffix check would
        // accept ANY AWS customer's bucket, including an attacker's own —
        // not just this app's. Bucket names are globally attacker-choosable,
        // so this must be an exact host match, not a suffix match.
        let result = validate_upload_url(
            "https://attacker-owned-bucket.s3.us-east-1.amazonaws.com/audio/foo.wav",
        );
        assert!(result.is_err(), "a different (even if AWS-hosted) bucket must be rejected");
    }
}
