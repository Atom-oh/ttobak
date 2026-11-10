//! AWS profile-name discovery from the shared credentials and config files.
//! Pure parsing only; no AWS SDK. Secrets are never read into the result.

use std::path::PathBuf;

fn valid_name(name: &str) -> bool {
    !name.is_empty()
        && name.len() <= 64
        && name
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || matches!(c, '_' | '.' | '@' | '-'))
}

/// Section headers (`[...]` contents) of an INI-like file, ignoring comments.
fn sections(text: &str) -> impl Iterator<Item = &str> {
    text.lines().filter_map(|line| {
        let line = line.trim();
        let inner = line.strip_prefix('[')?;
        let end = inner.find(']')?;
        Some(inner[..end].trim())
    })
}

/// Profile names from a credentials file (`[name]`) and a config file
/// (`[default]`, `[profile name]`; `[sso-session ..]` and `[services ..]`
/// are skipped). Deduplicated; `default` first, then sorted. Names outside
/// `^[A-Za-z0-9_.@-]{1,64}$` are dropped.
pub fn profile_names(credentials: &str, config: &str) -> Vec<String> {
    let mut names: Vec<String> = Vec::new();
    for section in sections(credentials) {
        names.push(section.to_string());
    }
    for section in sections(config) {
        if section == "default" {
            names.push(section.to_string());
        } else if let Some(rest) = section.strip_prefix("profile") {
            if rest.starts_with(char::is_whitespace) {
                names.push(rest.trim().to_string());
            }
        }
    }
    names.retain(|n| valid_name(n));
    names.sort();
    names.dedup();
    if let Some(pos) = names.iter().position(|n| n == "default") {
        let d = names.remove(pos);
        names.insert(0, d);
    }
    names
}

fn aws_path(env_var: &str, file: &str) -> Option<PathBuf> {
    if let Some(p) = std::env::var_os(env_var).filter(|p| !p.is_empty()) {
        return Some(PathBuf::from(p));
    }
    let home = std::env::var_os("HOME").filter(|h| !h.is_empty())?;
    Some(PathBuf::from(home).join(".aws").join(file))
}

fn read_or_empty(path: Option<PathBuf>) -> String {
    path.and_then(|p| std::fs::read_to_string(p).ok())
        .unwrap_or_default()
}

/// Profile names from the user's AWS files; unreadable files count as empty.
pub fn list() -> Vec<String> {
    let credentials = read_or_empty(aws_path("AWS_SHARED_CREDENTIALS_FILE", "credentials"));
    let config = read_or_empty(aws_path("AWS_CONFIG_FILE", "config"));
    profile_names(&credentials, &config)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lists_default_first() {
        let got = profile_names(
            "[cc-bedrock]\n[default]\n",
            "[profile dev]\n[sso-session x]\n",
        );
        assert_eq!(got, vec!["default", "cc-bedrock", "dev"]);
    }

    #[test]
    fn drops_unsafe_names() {
        let long = format!("[{}]\n", "a".repeat(65));
        let got = profile_names(&format!("[bad name]\n[ok.name@x-1]\n[a/b]\n{long}"), "");
        assert_eq!(got, vec!["ok.name@x-1"]);
    }

    #[test]
    fn empty_inputs() {
        assert!(profile_names("", "").is_empty());
    }

    #[test]
    fn config_default_and_services_ignored() {
        let got = profile_names("", "[default]\n[services s]\n[profile  p1 ]\n[profilex]\n");
        assert_eq!(got, vec!["default", "p1"]);
    }
}
