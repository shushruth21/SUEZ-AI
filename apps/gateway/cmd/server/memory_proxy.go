package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
)

// handleMemoryProxy forwards /v1/memory/* to the control plane's
// /api/v1/memory/*, so a client that only knows the gateway (the Context
// Guardian desktop app) can use SUEZ Memory. The caller's own credential is
// forwarded and the control plane authenticates and authorizes it again; the
// gateway adds no privilege. The internal token is never sent on this path.
func (a *gatewayApp) handleMemoryProxy(w http.ResponseWriter, r *http.Request) {
	identity := mustIdentity(r.Context())
	needed := "memory.read"
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		needed = "memory.write"
	}
	if identity.TenantID == "" || !hasPermission(identity, needed) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "permission denied", "requiredPermission": needed})
		return
	}
	cleaned := path.Clean(r.URL.Path)
	if !strings.HasPrefix(cleaned, "/v1/memory/") {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	target, err := url.Parse(strings.TrimRight(env("CONTROL_PLANE_INTERNAL_URL", "http://control-plane:8081"), "/"))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "memory service unavailable"})
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(out *httputil.ProxyRequest) {
			out.SetURL(target)
			out.Out.URL.Path = "/api/v1/memory/" + strings.TrimPrefix(cleaned, "/v1/memory/")
			out.Out.URL.RawPath = ""
			out.Out.Header.Del("X-SUEZ-Internal-Token")
			out.Out.Header.Del("Origin")
			out.Out.Header.Set("X-Request-Id", requestID(r.Context()))
		},
		ModifyResponse: func(resp *http.Response) error {
			// CORS is decided by this service, not copied from upstream.
			for name := range resp.Header {
				if strings.HasPrefix(strings.ToLower(name), "access-control-") {
					resp.Header.Del(name)
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			logWarn("memory proxy failed", requestID(r.Context()), err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "memory service unavailable"})
		},
	}
	proxy.ServeHTTP(w, r)
}
