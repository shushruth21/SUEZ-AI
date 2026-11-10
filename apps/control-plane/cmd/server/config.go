package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// runtimeConfig is read once at startup. SUEZ_ENV defaults to production so
// that a deployment which forgets to set it gets the strict behaviour;
// development conveniences must be asked for explicitly.
type runtimeConfig struct {
	Production            bool
	CORSOrigins           []string
	InternalToken         string
	ExposeDemoCredentials bool
	TrustedProxies        []*net.IPNet
	RateLimitRPM          int
}

const developmentInternalToken = "suez-development-only-internal-token"

func loadRuntimeConfig() (runtimeConfig, error) {
	cfg := runtimeConfig{RateLimitRPM: 600}
	switch strings.ToLower(strings.TrimSpace(env("SUEZ_ENV", "production"))) {
	case "production", "prod":
		cfg.Production = true
	case "development", "dev", "test":
	default:
		return cfg, errors.New("SUEZ_ENV must be production or development")
	}

	var problems []string
	for _, origin := range strings.Split(env("CORS_ALLOWED_ORIGINS", ""), ",") {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		if origin == "*" {
			if cfg.Production {
				problems = append(problems, "CORS_ALLOWED_ORIGINS must list explicit origins in production, not *")
			}
			cfg.CORSOrigins = []string{"*"}
			break
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Path != "" {
			problems = append(problems, fmt.Sprintf("CORS_ALLOWED_ORIGINS: %q is not an origin (scheme://host[:port])", origin))
			continue
		}
		if cfg.Production && parsed.Scheme != "https" {
			problems = append(problems, fmt.Sprintf("CORS_ALLOWED_ORIGINS: %q must use https in production", origin))
		}
		cfg.CORSOrigins = append(cfg.CORSOrigins, origin)
	}

	cfg.InternalToken = strings.TrimSpace(env("SUEZ_INTERNAL_TOKEN", ""))
	if cfg.InternalToken == "" && !cfg.Production {
		cfg.InternalToken = developmentInternalToken
	}
	if len(cfg.InternalToken) < 32 {
		problems = append(problems, "SUEZ_INTERNAL_TOKEN must be set to at least 32 characters (shared with the gateway)")
	}
	if cfg.Production && cfg.InternalToken == developmentInternalToken {
		problems = append(problems, "SUEZ_INTERNAL_TOKEN must not be the development default in production")
	}

	switch strings.ToLower(strings.TrimSpace(env("SUEZ_EXPOSE_DEMO_CREDENTIALS", ""))) {
	case "1", "true", "yes":
		if cfg.Production {
			problems = append(problems, "SUEZ_EXPOSE_DEMO_CREDENTIALS cannot be enabled in production")
		}
		cfg.ExposeDemoCredentials = true
	}

	for _, cidr := range strings.Split(env("TRUSTED_PROXY_CIDRS", ""), ",") {
		if cidr = strings.TrimSpace(cidr); cidr == "" {
			continue
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			problems = append(problems, fmt.Sprintf("TRUSTED_PROXY_CIDRS: %q is not a CIDR", cidr))
			continue
		}
		cfg.TrustedProxies = append(cfg.TrustedProxies, network)
	}
	if raw := strings.TrimSpace(env("RATE_LIMIT_RPM", "")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			problems = append(problems, "RATE_LIMIT_RPM must be a non-negative integer")
		}
		cfg.RateLimitRPM = value
	}

	if cfg.Production {
		database := env("DATABASE_URL", "")
		if database == "" {
			problems = append(problems, "DATABASE_URL is required in production")
		} else if parsed, err := url.Parse(database); err != nil {
			problems = append(problems, "DATABASE_URL is not a valid URL")
		} else if password, _ := parsed.User.Password(); password == "" || password == "suez" || password == "postgres" || password == "password" {
			problems = append(problems, "DATABASE_URL must not use an empty or default password in production")
		}
		if strings.TrimSpace(env("JWT_MODE", "")) == "" {
			problems = append(problems, "JWT_MODE must be set explicitly in production (jwks or disabled)")
		}
		if strings.TrimSpace(env("SUEZ_DATA_KEY", "")) == "" {
			problems = append(problems, "SUEZ_DATA_KEY (or SUEZ_DATA_KEY_FILE) is required in production")
		}
	}
	if len(problems) > 0 {
		return cfg, errors.New("configuration is not valid:\n  - " + strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

func newLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

type requestIDKey struct{}

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func newRequestID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// withRequestContext assigns the correlation ID, sets response security
// headers and writes one structured access-log line. Only the path is logged:
// query strings, headers and bodies can carry credentials or user content.
func (c runtimeConfig) withRequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !requestIDPattern.MatchString(id) {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")

		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(recorder, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
		slog.Info("request",
			"requestId", id,
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"durationMs", time.Since(start).Milliseconds(),
			"client", c.clientIP(r),
		)
	})
}

// withCORS answers only for origins on the allowlist. With no allowlist no
// CORS headers are sent at all, which is right for same-origin deployments
// behind the edge.
func (c runtimeConfig) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && c.originAllowed(origin) {
			if len(c.CORSOrigins) == 1 && c.CORSOrigins[0] == "*" {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, X-SUEZ-Session-Id, X-Request-Id")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id, X-SUEZ-Trace-Id, X-Suez-Cache, X-SUEZ-Context-Status")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (c runtimeConfig) originAllowed(origin string) bool {
	for _, allowed := range c.CORSOrigins {
		if allowed == "*" || allowed == origin {
			return true
		}
	}
	return false
}

// clientIP uses X-Forwarded-For only when the direct peer is a trusted proxy;
// otherwise a caller could pick its own address and dodge rate limits.
func (c runtimeConfig) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || !c.trusted(peer) {
		return host
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(forwarded) - 1; i >= 0; i-- {
		candidate := net.ParseIP(strings.TrimSpace(forwarded[i]))
		if candidate == nil {
			break
		}
		if !c.trusted(candidate) {
			return candidate.String()
		}
	}
	return host
}

func (c runtimeConfig) trusted(ip net.IP) bool {
	for _, network := range c.TrustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// requireInternalToken protects /internal routes, which the gateway calls
// over the private network. The edge also refuses these paths.
func (c runtimeConfig) requireInternalToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		presented := r.Header.Get("X-SUEZ-Internal-Token")
		if subtle.ConstantTimeCompare([]byte(presented), []byte(c.InternalToken)) != 1 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		next(w, r)
	}
}

// rateLimiter is a fixed-window counter per client address. It bounds
// credential guessing and accidental floods against one control-plane
// instance; the gateway's Redis quotas are the cross-instance limits.
type rateLimiter struct {
	mu      sync.Mutex
	window  time.Time
	counts  map[string]int
	perMin  int
	clientF func(*http.Request) string
}

func (c runtimeConfig) withRateLimit(next http.Handler) http.Handler {
	if c.RateLimitRPM == 0 {
		return next
	}
	limiter := &rateLimiter{counts: map[string]int{}, perMin: c.RateLimitRPM, clientF: c.clientIP}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.allow(r) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *rateLimiter) allow(r *http.Request) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().Truncate(time.Minute)
	if !now.Equal(l.window) {
		l.window, l.counts = now, map[string]int{}
	}
	client := l.clientF(r)
	l.counts[client]++
	return l.counts[client] <= l.perMin
}

func decodeDataKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err := encoding.DecodeString(raw); err == nil && len(key) == 32 {
			return key, nil
		}
	}
	if key, err := hex.DecodeString(raw); err == nil && len(key) == 32 {
		return key, nil
	}
	return nil, errors.New("SUEZ_DATA_KEY must be 32 random bytes, base64 or hex encoded")
}
