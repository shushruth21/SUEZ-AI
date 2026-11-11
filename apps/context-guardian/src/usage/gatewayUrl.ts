// Mirrors normalize_base_url in src-tauri/src/gateway.rs so the settings form
// can explain a bad URL before the native side rejects it.

function isLoopbackHost(hostname: string): boolean {
  const host = hostname.toLowerCase().replace(/\.$/, "");
  if (host === "localhost" || host.endsWith(".localhost")) return true;
  if (host === "[::1]") return true;
  return /^127(\.\d{1,3}){3}$/.test(host);
}

/** Returns a user-facing problem with the gateway URL, or null when it is acceptable. */
export function gatewayUrlProblem(raw: string): string | null {
  let url: URL;
  try {
    url = new URL(raw.trim());
  } catch {
    return "Gateway URL must start with http:// or https://.";
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    return "Gateway URL must start with http:// or https://.";
  }
  if (!url.hostname) return "Gateway URL has no host.";
  if (url.username || url.password) {
    return "Gateway URL must not contain credentials; store the API key separately.";
  }
  if (url.search || url.hash) return "Gateway URL must not contain a query or fragment.";
  if (url.protocol === "http:" && !isLoopbackHost(url.hostname)) {
    return "Use https:// for a gateway that is not on this machine; the API key would otherwise be sent unencrypted.";
  }
  return null;
}
