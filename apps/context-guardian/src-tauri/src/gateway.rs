//! SUEZ gateway client and API-key storage.
//!
//! The API key lives only in the OS credential store (macOS Keychain). It is
//! read here, sent as the X-API-Key header and never returned to the
//! frontend, written to disk, logged or included in error messages.

use serde::{Deserialize, Serialize};

use std::time::Duration;

pub const DEFAULT_KEYCHAIN_SERVICE: &str = "ai.acrosystems.suez.contextguardian";
const KEYCHAIN_ACCOUNT: &str = "gateway-api-key";
pub const REQUEST_TIMEOUT: Duration = Duration::from_secs(5);
const CONNECT_TIMEOUT: Duration = Duration::from_secs(3);
const MAX_ERROR_CHARS: usize = 300;

pub trait SecretStore: Send + Sync {
    fn get(&self) -> Result<Option<String>, String>;
    fn set(&self, secret: &str) -> Result<(), String>;
    fn clear(&self) -> Result<(), String>;
}

pub struct KeychainStore {
    service: String,
}

impl KeychainStore {
    pub fn new(service: impl Into<String>) -> Self {
        Self { service: service.into() }
    }

    fn entry(&self) -> Result<keyring::Entry, String> {
        keyring::Entry::new(&self.service, KEYCHAIN_ACCOUNT).map_err(|e| format!("keychain unavailable: {e}"))
    }
}

impl SecretStore for KeychainStore {
    fn get(&self) -> Result<Option<String>, String> {
        match self.entry()?.get_password() {
            Ok(secret) => Ok(Some(secret)),
            Err(keyring::Error::NoEntry) => Ok(None),
            Err(e) => Err(format!("could not read the API key from the keychain: {e}")),
        }
    }

    fn set(&self, secret: &str) -> Result<(), String> {
        self.entry()?
            .set_password(secret)
            .map_err(|e| format!("could not store the API key in the keychain: {e}"))
    }

    fn clear(&self) -> Result<(), String> {
        match self.entry()?.delete_credential() {
            Ok(()) | Err(keyring::Error::NoEntry) => Ok(()),
            Err(e) => Err(format!("could not remove the API key from the keychain: {e}")),
        }
    }
}

/// In-memory store used by unit tests.
#[cfg(test)]
#[derive(Default)]
pub struct MemoryStore(std::sync::Mutex<Option<String>>);

#[cfg(test)]
impl SecretStore for MemoryStore {
    fn get(&self) -> Result<Option<String>, String> {
        Ok(self.0.lock().unwrap().clone())
    }
    fn set(&self, secret: &str) -> Result<(), String> {
        *self.0.lock().unwrap() = Some(secret.to_string());
        Ok(())
    }
    fn clear(&self) -> Result<(), String> {
        *self.0.lock().unwrap() = None;
        Ok(())
    }
}

/// Refuses `payload` (serialized JSON about to be written to disk) if it
/// contains the stored gateway API key, raw or JSON-escaped. The key itself
/// never appears in the error. If the Keychain cannot be read, nothing can be
/// compared and the save goes ahead (a Keychain failure must not block saving
/// a checkpoint draft).
pub fn refuse_if_contains_secret(secrets: &dyn SecretStore, payload: &str) -> Result<(), String> {
    let Ok(Some(key)) = secrets.get() else { return Ok(()) };
    if key.is_empty() {
        return Ok(());
    }
    let escaped = serde_json::to_string(&key).unwrap_or_default();
    let escaped = escaped.trim_matches('"');
    if payload.contains(&key) || (!escaped.is_empty() && payload.contains(escaped)) {
        return Err("refused to save: the text contains the stored gateway API key. Remove it and try again.".into());
    }
    Ok(())
}

pub fn validate_api_key(key: &str) -> Result<String, String> {
    let key = key.trim();
    if key.is_empty() {
        return Err("API key is empty".into());
    }
    if key.len() > 512 {
        return Err("API key is longer than 512 characters".into());
    }
    if key.chars().any(|c| c.is_control() || c == ' ') {
        return Err("API key contains spaces or control characters".into());
    }
    Ok(key.to_string())
}

/// Accepts http(s)://host[:port][/path-prefix]; rejects credentials, queries
/// and fragments so nothing sensitive can hide in the URL.
pub fn normalize_base_url(raw: &str) -> Result<String, String> {
    let parsed = url::Url::parse(raw.trim()).map_err(|_| "gateway URL is not a valid URL".to_string())?;
    if parsed.scheme() != "http" && parsed.scheme() != "https" {
        return Err("gateway URL must start with http:// or https://".into());
    }
    if parsed.host_str().is_none() {
        return Err("gateway URL has no host".into());
    }
    if !parsed.username().is_empty() || parsed.password().is_some() {
        return Err("gateway URL must not contain credentials; store the API key separately".into());
    }
    if parsed.query().is_some() || parsed.fragment().is_some() {
        return Err("gateway URL must not contain a query or fragment".into());
    }
    Ok(parsed.as_str().trim_end_matches('/').to_string())
}

/// Mirrors the gateway's GET /v1/context/usage response.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ContextUsageMeasurement {
    pub model: String,
    pub provider: String,
    pub source: String,
    pub cache_status: String,
    pub prompt_tokens: Option<i64>,
    pub completion_tokens: Option<i64>,
    pub context_tokens: Option<i64>,
    #[serde(default)]
    pub trace_id: Option<String>,
    pub measured_at: String,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ContextUsage {
    pub session_id: String,
    pub status: String,
    #[serde(default)]
    pub message: String,
    pub measurement: Option<ContextUsageMeasurement>,
    pub context_window: Option<i64>,
    pub percent: Option<f64>,
    pub retrieved_at: String,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct GatewayIdentity {
    pub actor: String,
    pub tenant_slug: String,
    pub auth_method: String,
}

/// Every outcome of a gateway call, tagged for the frontend. None of the
/// messages contain the API key.
#[derive(Debug, Clone, PartialEq, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum GatewayResult<T> {
    Ok { data: T },
    NotConfigured { message: String },
    AuthFailed { message: String },
    NetworkError { message: String },
    GatewayError { status: u16, message: String },
    InvalidResponse { message: String },
}

pub fn build_client(timeout: Duration) -> reqwest::Client {
    reqwest::Client::builder()
        .timeout(timeout)
        .connect_timeout(CONNECT_TIMEOUT.min(timeout))
        .build()
        .expect("failed to build HTTP client")
}

fn truncate(text: &str) -> String {
    let clean: String = text.chars().filter(|c| !c.is_control()).collect();
    if clean.chars().count() > MAX_ERROR_CHARS {
        format!("{}…", clean.chars().take(MAX_ERROR_CHARS).collect::<String>())
    } else {
        clean
    }
}

fn scrub(message: String, key: &str) -> String {
    if key.is_empty() {
        message
    } else {
        message.replace(key, "[redacted]")
    }
}

async fn get_json<T: for<'de> Deserialize<'de>>(
    client: &reqwest::Client,
    secrets: &dyn SecretStore,
    base_url: &str,
    path: &str,
    query: &[(&str, &str)],
) -> GatewayResult<T> {
    let base = match normalize_base_url(base_url) {
        Ok(base) => base,
        Err(message) => return GatewayResult::NotConfigured { message },
    };
    let key = match secrets.get() {
        Ok(Some(key)) => key,
        Ok(None) => {
            return GatewayResult::NotConfigured {
                message: "No gateway API key is stored. Add one in Settings.".into(),
            }
        }
        Err(message) => return GatewayResult::NotConfigured { message },
    };

    let mut url = match url::Url::parse(&format!("{base}{path}")) {
        Ok(url) => url,
        Err(_) => return GatewayResult::NotConfigured { message: "gateway URL is not valid".into() },
    };
    url.query_pairs_mut().extend_pairs(query);

    let response = match client.get(url).header("X-API-Key", &key).send().await {
        Ok(response) => response,
        Err(error) => {
            let what = if error.is_timeout() {
                format!("Gateway did not respond within {} s", REQUEST_TIMEOUT.as_secs())
            } else if error.is_connect() {
                format!("Could not connect to the gateway at {base}")
            } else {
                format!("Request to the gateway failed: {error}")
            };
            return GatewayResult::NetworkError { message: scrub(what, &key) };
        }
    };

    let status = response.status();
    let body = match response.text().await {
        Ok(body) => body,
        Err(error) => {
            return GatewayResult::NetworkError {
                message: scrub(format!("Could not read the gateway response: {error}"), &key),
            }
        }
    };

    if status.is_success() {
        return match serde_json::from_str::<T>(&body) {
            Ok(data) => GatewayResult::Ok { data },
            Err(error) => GatewayResult::InvalidResponse {
                message: scrub(format!("Unexpected response from the gateway: {error}"), &key),
            },
        };
    }

    let gateway_message = serde_json::from_str::<serde_json::Value>(&body)
        .ok()
        .and_then(|v| v.get("error").and_then(|e| e.as_str()).map(str::to_string))
        .unwrap_or_else(|| status.canonical_reason().unwrap_or("error").to_string());
    let message = scrub(truncate(&gateway_message), &key);
    if status.as_u16() == 401 || status.as_u16() == 403 {
        GatewayResult::AuthFailed {
            message: format!("The gateway rejected the API key ({}): {message}", status.as_u16()),
        }
    } else {
        GatewayResult::GatewayError { status: status.as_u16(), message }
    }
}

pub async fn fetch_context_usage(
    client: &reqwest::Client,
    secrets: &dyn SecretStore,
    base_url: &str,
    session_id: &str,
) -> GatewayResult<ContextUsage> {
    get_json(client, secrets, base_url, "/v1/context/usage", &[("session", session_id)]).await
}

pub async fn fetch_identity(
    client: &reqwest::Client,
    secrets: &dyn SecretStore,
    base_url: &str,
) -> GatewayResult<GatewayIdentity> {
    #[derive(Deserialize)]
    #[serde(rename_all = "camelCase")]
    struct Envelope {
        identity_context: GatewayIdentity,
    }
    match get_json::<Envelope>(client, secrets, base_url, "/v1/identity", &[]).await {
        GatewayResult::Ok { data } => GatewayResult::Ok { data: data.identity_context },
        GatewayResult::NotConfigured { message } => GatewayResult::NotConfigured { message },
        GatewayResult::AuthFailed { message } => GatewayResult::AuthFailed { message },
        GatewayResult::NetworkError { message } => GatewayResult::NetworkError { message },
        GatewayResult::GatewayError { status, message } => GatewayResult::GatewayError { status, message },
        GatewayResult::InvalidResponse { message } => GatewayResult::InvalidResponse { message },
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::net::TcpListener;
    use std::sync::mpsc;
    use std::thread;

    const KEY: &str = "sk_test_super_secret_value_123";

    /// Minimal one-shot HTTP server: captures the request, replies with `response`.
    fn serve_once(response: String, delay: Duration) -> (String, mpsc::Receiver<String>) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        let (tx, rx) = mpsc::channel();
        thread::spawn(move || {
            if let Ok((mut stream, _)) = listener.accept() {
                let mut buf = [0u8; 8192];
                let n = stream.read(&mut buf).unwrap_or(0);
                let _ = tx.send(String::from_utf8_lossy(&buf[..n]).to_string());
                thread::sleep(delay);
                let _ = stream.write_all(response.as_bytes());
            }
        });
        (format!("http://{addr}"), rx)
    }

    fn http(status: &str, body: &str) -> String {
        format!(
            "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
            body.len()
        )
    }

    fn store_with_key() -> MemoryStore {
        let store = MemoryStore::default();
        store.set(KEY).unwrap();
        store
    }

    fn run<F: std::future::Future>(future: F) -> F::Output {
        tauri::async_runtime::block_on(future)
    }

    fn assert_no_key<T: Serialize>(result: &T) {
        let json = serde_json::to_string(result).unwrap();
        assert!(!json.contains(KEY), "API key leaked into result: {json}");
    }

    const OK_BODY: &str = r#"{"sessionId":"s1","status":"ok","message":"m","measurement":{"model":"demo-model","provider":"stub","source":"gateway_estimate","cacheStatus":"miss","promptTokens":800,"completionTokens":60,"contextTokens":860,"traceId":"t1","measuredAt":"2026-10-09T12:00:00Z"},"contextWindow":1000,"percent":86.0,"retrievedAt":"2026-10-09T12:00:01Z"}"#;

    #[test]
    fn validates_api_keys() {
        assert_eq!(validate_api_key("  abc  ").unwrap(), "abc");
        assert!(validate_api_key("").is_err());
        assert!(validate_api_key("has space").is_err());
        assert!(validate_api_key("new\nline").is_err());
        assert!(validate_api_key(&"x".repeat(513)).is_err());
    }

    #[test]
    fn normalizes_and_rejects_base_urls() {
        assert_eq!(normalize_base_url("http://localhost:8080/").unwrap(), "http://localhost:8080");
        assert_eq!(normalize_base_url(" https://host:5443/gateway/ ").unwrap(), "https://host:5443/gateway");
        for bad in ["", "localhost:8080", "ftp://host", "http://user:pw@host", "http://host/?q=1", "http://host/#x"] {
            assert!(normalize_base_url(bad).is_err(), "{bad:?} should be rejected");
        }
    }

    #[test]
    fn fetch_sends_key_header_and_session_and_parses_ok() {
        let (base, request) = serve_once(http("200 OK", OK_BODY), Duration::ZERO);
        let result = run(fetch_context_usage(&build_client(REQUEST_TIMEOUT), &store_with_key(), &base, "s1"));
        let raw = request.recv().unwrap();
        assert!(raw.starts_with("GET /v1/context/usage?session=s1 HTTP/1.1"), "{raw}");
        assert!(raw.to_lowercase().contains(&format!("x-api-key: {KEY}")), "{raw}");
        match result {
            GatewayResult::Ok { data } => {
                assert_eq!(data.percent, Some(86.0));
                assert_eq!(data.measurement.unwrap().context_tokens, Some(860));
            }
            other => panic!("unexpected {other:?}"),
        }
    }

    #[test]
    fn session_id_is_url_encoded() {
        let (base, request) = serve_once(http("200 OK", OK_BODY), Duration::ZERO);
        let _ = run(fetch_context_usage(&build_client(REQUEST_TIMEOUT), &store_with_key(), &base, "a b&c"));
        assert!(request.recv().unwrap().starts_with("GET /v1/context/usage?session=a+b%26c "));
    }

    #[test]
    fn auth_failure_is_reported_without_the_key() {
        let body = format!(r#"{{"error":"invalid api key {KEY}"}}"#);
        let (base, _) = serve_once(http("401 Unauthorized", &body), Duration::ZERO);
        let result = run(fetch_context_usage(&build_client(REQUEST_TIMEOUT), &store_with_key(), &base, "s1"));
        assert!(matches!(result, GatewayResult::AuthFailed { .. }), "{result:?}");
        assert_no_key(&result);
    }

    #[test]
    fn gateway_errors_carry_status_and_message() {
        let (base, _) = serve_once(http("503 Service Unavailable", r#"{"error":"apply migration 0006"}"#), Duration::ZERO);
        let result = run(fetch_context_usage(&build_client(REQUEST_TIMEOUT), &store_with_key(), &base, "s1"));
        assert_eq!(result, GatewayResult::GatewayError { status: 503, message: "apply migration 0006".into() });
    }

    #[test]
    fn invalid_json_is_an_invalid_response() {
        let (base, _) = serve_once(http("200 OK", "not json"), Duration::ZERO);
        let result = run(fetch_context_usage(&build_client(REQUEST_TIMEOUT), &store_with_key(), &base, "s1"));
        assert!(matches!(result, GatewayResult::InvalidResponse { .. }), "{result:?}");
    }

    #[test]
    fn connection_refused_is_a_network_error() {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        drop(listener);
        let result = run(fetch_context_usage(&build_client(REQUEST_TIMEOUT), &store_with_key(), &base, "s1"));
        assert!(matches!(result, GatewayResult::NetworkError { .. }), "{result:?}");
        assert_no_key(&result);
    }

    #[test]
    fn slow_gateway_times_out() {
        let (base, _) = serve_once(http("200 OK", OK_BODY), Duration::from_millis(1500));
        let result = run(fetch_context_usage(&build_client(Duration::from_millis(300)), &store_with_key(), &base, "s1"));
        assert!(matches!(result, GatewayResult::NetworkError { .. }), "{result:?}");
    }

    #[test]
    fn missing_key_or_url_is_not_configured_and_makes_no_request() {
        let result = run(fetch_context_usage(&build_client(REQUEST_TIMEOUT), &MemoryStore::default(), "http://127.0.0.1:9", "s1"));
        assert!(matches!(result, GatewayResult::NotConfigured { .. }));
        let result = run(fetch_context_usage(&build_client(REQUEST_TIMEOUT), &store_with_key(), "", "s1"));
        assert!(matches!(result, GatewayResult::NotConfigured { .. }));
    }

    #[test]
    fn identity_is_unwrapped() {
        let body = r#"{"identityContext":{"tenantId":"t","tenantSlug":"demo","actor":"Bootstrap Admin Key","authMethod":"api_key","roles":["admin"]}}"#;
        let (base, _) = serve_once(http("200 OK", body), Duration::ZERO);
        let result = run(fetch_identity(&build_client(REQUEST_TIMEOUT), &store_with_key(), &base));
        assert_eq!(
            result,
            GatewayResult::Ok {
                data: GatewayIdentity { actor: "Bootstrap Admin Key".into(), tenant_slug: "demo".into(), auth_method: "api_key".into() }
            }
        );
    }

    /// Touches the real login keychain under a throwaway service name.
    /// Run explicitly: cargo test -- --ignored keychain
    #[test]
    #[ignore]
    fn keychain_round_trip() {
        let store = KeychainStore::new(format!("{DEFAULT_KEYCHAIN_SERVICE}.test-{}", std::process::id()));
        store.clear().unwrap();
        assert_eq!(store.get().unwrap(), None);
        store.set(KEY).unwrap();
        assert_eq!(store.get().unwrap().as_deref(), Some(KEY));
        store.clear().unwrap();
        assert_eq!(store.get().unwrap(), None);
    }

    #[test]
    fn secret_guard_refuses_payloads_containing_the_stored_key() {
        let secrets = MemoryStore(std::sync::Mutex::new(None));
        assert!(refuse_if_contains_secret(&secrets, "anything").is_ok());
        secrets.set("sk-test-\\\"quoted").unwrap();
        let raw = serde_json::json!({ "notes": "key is sk-test-\\\"quoted here" }).to_string();
        let err = refuse_if_contains_secret(&secrets, &raw).unwrap_err();
        assert!(!err.contains("sk-test"), "error must not echo the key");
        assert!(refuse_if_contains_secret(&secrets, r#"{"notes":"sk-test-\\\"quoted"}"#).is_err());
        assert!(refuse_if_contains_secret(&secrets, r#"{"notes":"no secrets here"}"#).is_ok());
    }
}
