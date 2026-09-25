//! Wire protocol of the local control socket (ADR-046): one newline-terminated
//! JSON request per connection, one JSON response line back.
//!
//! Request:  `{"v":1,"id":"<1-64 [A-Za-z0-9_-]>","action":"status"|"start"|"stop", ...}`
//! - start: `title` (1-200 chars, required), `accountId?`
//! - stop:  `upload?` (default true)
//!
//! Response: `{"v":1,"id":...,"ok":true,"data":{...}}` or
//! `{"v":1,"id":...,"ok":false,"error":{"code":...,"message":...}}`.
//!
//! Pure parsing/validation only, so the rules are unit tested off macOS.

use serde_json::{json, Map, Value};

pub const PROTOCOL_VERSION: u64 = 1;
/// Largest accepted request line, including the newline.
pub const MAX_REQUEST_BYTES: usize = 16 * 1024;
/// Largest SPA-supplied reply or state object forwarded to a client.
pub const MAX_SPA_PAYLOAD_BYTES: usize = 16 * 1024;
pub const MAX_TITLE_CHARS: usize = 200;
pub const MAX_ACCOUNT_ID_CHARS: usize = 128;

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Action {
    Status,
    Start {
        title: String,
        account_id: Option<String>,
    },
    Stop {
        upload: bool,
    },
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Request {
    pub id: String,
    pub action: Action,
}

impl Action {
    pub fn name(&self) -> &'static str {
        match self {
            Action::Status => "status",
            Action::Start { .. } => "start",
            Action::Stop { .. } => "stop",
        }
    }

    /// Parameters handed to the SPA (camelCase, JSON).
    pub fn params(&self) -> Value {
        match self {
            Action::Status => json!({}),
            Action::Start { title, account_id } => {
                let mut m = Map::new();
                m.insert("title".into(), json!(title));
                if let Some(a) = account_id {
                    m.insert("accountId".into(), json!(a));
                }
                Value::Object(m)
            }
            Action::Stop { upload } => json!({ "upload": upload }),
        }
    }
}

/// Machine-readable error codes shared with the MCP client.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Code {
    BadRequest,
    UnsupportedVersion,
    AppNotReady,
    Timeout,
    Internal,
}

impl Code {
    pub fn as_str(self) -> &'static str {
        match self {
            Code::BadRequest => "bad_request",
            Code::UnsupportedVersion => "unsupported_version",
            Code::AppNotReady => "app_not_ready",
            Code::Timeout => "timeout",
            Code::Internal => "internal",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ProtoError {
    pub code: Code,
    pub message: String,
    /// Request id when it could be read, so the error can be correlated.
    pub id: Option<String>,
}

fn err(code: Code, message: impl Into<String>, id: Option<&str>) -> ProtoError {
    ProtoError {
        code,
        message: message.into(),
        id: id.map(str::to_owned),
    }
}

fn valid_id(id: &str) -> bool {
    !id.is_empty()
        && id.len() <= 64
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
}

fn bounded_text(value: &str, max_chars: usize) -> bool {
    let trimmed = value.trim();
    !trimmed.is_empty()
        && trimmed.chars().count() <= max_chars
        && !trimmed.chars().any(|c| c.is_control())
}

pub fn parse_request(line: &[u8]) -> Result<Request, ProtoError> {
    if line.len() > MAX_REQUEST_BYTES {
        return Err(err(Code::BadRequest, "request too large", None));
    }
    let value: Value = serde_json::from_slice(line)
        .map_err(|_| err(Code::BadRequest, "request is not valid JSON", None))?;
    let obj = value
        .as_object()
        .ok_or_else(|| err(Code::BadRequest, "request must be a JSON object", None))?;
    let id = obj
        .get("id")
        .and_then(Value::as_str)
        .filter(|id| valid_id(id));
    let Some(id) = id else {
        return Err(err(
            Code::BadRequest,
            "id must be 1-64 characters of [A-Za-z0-9_-]",
            None,
        ));
    };
    match obj.get("v").and_then(Value::as_u64) {
        Some(PROTOCOL_VERSION) => {}
        _ => return Err(err(Code::UnsupportedVersion, "v must be 1", Some(id))),
    }
    let action = match obj.get("action").and_then(Value::as_str) {
        Some("status") => Action::Status,
        Some("start") => {
            let title = obj.get("title").and_then(Value::as_str).unwrap_or_default();
            if !bounded_text(title, MAX_TITLE_CHARS) {
                return Err(err(
                    Code::BadRequest,
                    "title must be 1-200 characters",
                    Some(id),
                ));
            }
            let account_id = match obj.get("accountId") {
                None | Some(Value::Null) => None,
                Some(Value::String(a)) if bounded_text(a, MAX_ACCOUNT_ID_CHARS) => {
                    Some(a.trim().to_owned())
                }
                _ => {
                    return Err(err(
                        Code::BadRequest,
                        "accountId must be a non-empty string",
                        Some(id),
                    ))
                }
            };
            Action::Start {
                title: title.trim().to_owned(),
                account_id,
            }
        }
        Some("stop") => match obj.get("upload") {
            None | Some(Value::Null) => Action::Stop { upload: true },
            Some(Value::Bool(upload)) => Action::Stop { upload: *upload },
            _ => return Err(err(Code::BadRequest, "upload must be a boolean", Some(id))),
        },
        _ => {
            return Err(err(
                Code::BadRequest,
                "action must be status, start or stop",
                Some(id),
            ))
        }
    };
    Ok(Request {
        id: id.to_owned(),
        action,
    })
}

/// Validates a reply or state object coming back from the SPA before it is
/// forwarded to a socket client: bounded, and for replies `{ok, data?|error?}`
/// with a string error code.
pub fn validate_spa_reply(reply: &Value) -> Result<(), String> {
    if serde_json::to_vec(reply)
        .map(|b| b.len())
        .unwrap_or(usize::MAX)
        > MAX_SPA_PAYLOAD_BYTES
    {
        return Err("reply too large".into());
    }
    let obj = reply.as_object().ok_or("reply must be an object")?;
    match obj.get("ok") {
        Some(Value::Bool(true)) => Ok(()),
        Some(Value::Bool(false)) => match obj.get("error") {
            Some(Value::Object(e)) if e.get("code").map(Value::is_string).unwrap_or(false) => {
                Ok(())
            }
            _ => Err("failed reply needs error.code".into()),
        },
        _ => Err("reply needs boolean ok".into()),
    }
}

/// Serializes a response line (always newline-terminated).
pub fn response_line(id: Option<&str>, body: Value) -> String {
    let mut obj = Map::new();
    obj.insert("v".into(), json!(PROTOCOL_VERSION));
    obj.insert("id".into(), id.map_or(Value::Null, |i| json!(i)));
    if let Value::Object(fields) = body {
        obj.extend(fields);
    }
    let mut line = Value::Object(obj).to_string();
    line.push('\n');
    line
}

pub fn ok_line(id: &str, data: Value) -> String {
    response_line(Some(id), json!({ "ok": true, "data": data }))
}

pub fn error_line(id: Option<&str>, code: &str, message: &str) -> String {
    response_line(
        id,
        json!({ "ok": false, "error": { "code": code, "message": message } }),
    )
}

/// Forwards an SPA reply: `{ok, data?, error?}` merged under v/id.
pub fn spa_reply_line(id: &str, reply: &Value) -> String {
    match validate_spa_reply(reply) {
        Ok(()) => response_line(Some(id), reply.clone()),
        Err(reason) => error_line(
            Some(id),
            Code::Internal.as_str(),
            &format!("invalid app reply: {reason}"),
        ),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn parse(s: &str) -> Result<Request, ProtoError> {
        parse_request(s.as_bytes())
    }

    #[test]
    fn parses_each_action() {
        assert_eq!(
            parse(r#"{"v":1,"id":"a1","action":"status"}"#)
                .unwrap()
                .action,
            Action::Status
        );
        let start =
            parse(r#"{"v":1,"id":"a-2","action":"start","title":" 고객 미팅 ","accountId":"acc"}"#)
                .unwrap();
        assert_eq!(
            start.action,
            Action::Start {
                title: "고객 미팅".into(),
                account_id: Some("acc".into())
            }
        );
        assert_eq!(
            start.action.params(),
            json!({"title":"고객 미팅","accountId":"acc"})
        );
        assert_eq!(
            parse(r#"{"v":1,"id":"x","action":"stop"}"#).unwrap().action,
            Action::Stop { upload: true }
        );
        assert_eq!(
            parse(r#"{"v":1,"id":"x","action":"stop","upload":false}"#)
                .unwrap()
                .action,
            Action::Stop { upload: false }
        );
    }

    #[test]
    fn rejects_malformed_requests_with_codes() {
        let cases = [
            ("not json", Code::BadRequest, None),
            ("[]", Code::BadRequest, None),
            (r#"{"v":1,"action":"status"}"#, Code::BadRequest, None),
            (
                r#"{"v":1,"id":"../x","action":"status"}"#,
                Code::BadRequest,
                None,
            ),
            (
                r#"{"v":2,"id":"a","action":"status"}"#,
                Code::UnsupportedVersion,
                Some("a"),
            ),
            (
                r#"{"v":1,"id":"a","action":"delete"}"#,
                Code::BadRequest,
                Some("a"),
            ),
            (
                r#"{"v":1,"id":"a","action":"start"}"#,
                Code::BadRequest,
                Some("a"),
            ),
            (
                r#"{"v":1,"id":"a","action":"start","title":"   "}"#,
                Code::BadRequest,
                Some("a"),
            ),
            (
                r#"{"v":1,"id":"a","action":"start","title":"a\nb"}"#,
                Code::BadRequest,
                Some("a"),
            ),
            (
                r#"{"v":1,"id":"a","action":"start","title":"t","accountId":5}"#,
                Code::BadRequest,
                Some("a"),
            ),
            (
                r#"{"v":1,"id":"a","action":"stop","upload":"yes"}"#,
                Code::BadRequest,
                Some("a"),
            ),
        ];
        for (input, code, id) in cases {
            let e = parse(input).expect_err(input);
            assert_eq!(e.code, code, "{input}");
            assert_eq!(e.id.as_deref(), id, "{input}");
        }
    }

    #[test]
    fn enforces_size_limits() {
        let title = "t".repeat(201);
        assert!(parse(&format!(
            r#"{{"v":1,"id":"a","action":"start","title":"{title}"}}"#
        ))
        .is_err());
        let huge = vec![b' '; MAX_REQUEST_BYTES + 1];
        assert_eq!(parse_request(&huge).unwrap_err().code, Code::BadRequest);
    }

    #[test]
    fn response_lines_are_single_json_lines() {
        let ok = ok_line("a", json!({"recording": false}));
        assert!(ok.ends_with('\n') && ok.matches('\n').count() == 1);
        let v: Value = serde_json::from_str(ok.trim_end()).unwrap();
        assert_eq!(
            v,
            json!({"v":1,"id":"a","ok":true,"data":{"recording":false}})
        );
        let e: Value =
            serde_json::from_str(error_line(None, "bad_request", "x\ny").trim_end()).unwrap();
        assert_eq!(e["id"], Value::Null);
        assert_eq!(e["error"]["message"], "x\ny");
    }

    #[test]
    fn spa_replies_are_validated_before_forwarding() {
        let good = json!({"ok": true, "data": {"meetingId": "m1"}});
        let v: Value = serde_json::from_str(spa_reply_line("a", &good).trim_end()).unwrap();
        assert_eq!(v["data"]["meetingId"], "m1");
        let failed =
            json!({"ok": false, "error": {"code": "busy", "message": "recording in progress"}});
        assert!(validate_spa_reply(&failed).is_ok());
        for bad in [
            json!("x"),
            json!({"ok": "yes"}),
            json!({"ok": false}),
            json!({"ok": false, "error": {"code": 1}}),
        ] {
            let v: Value = serde_json::from_str(spa_reply_line("a", &bad).trim_end()).unwrap();
            assert_eq!(v["error"]["code"], "internal", "{bad}");
        }
        let big = json!({"ok": true, "data": "x".repeat(MAX_SPA_PAYLOAD_BYTES)});
        assert!(validate_spa_reply(&big).is_err());
    }
}
