package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	jwtModeDisabled = "disabled"
	jwtModeJWKS     = "jwks"
	// jwtModeDevHS256 verifies tokens with a shared secret. It exists so local
	// demos have signed tokens without an identity provider and is refused when
	// SUEZ_ENV is production.
	jwtModeDevHS256 = "dev-hs256"
)

var errJWTInvalid = errors.New("invalid token")

type jwtConfig struct {
	Mode            string
	Issuer          string
	Audiences       []string
	JWKSURL         string
	Algorithms      []string
	Skew            time.Duration
	JWKSCacheTTL    time.Duration
	DevSecret       []byte
	TenantIDClaim   string
	TenantSlugClaim string
	AllowHTTP       bool
}

// asymmetricJWTAlgorithms are the only algorithms accepted in jwks mode.
// "none" and the HMAC family are never accepted there: with a public JWKS an
// HMAC token could be forged using the public key as the secret.
var asymmetricJWTAlgorithms = map[string]bool{
	"RS256": true, "RS384": true, "RS512": true,
	"PS256": true, "PS384": true, "PS512": true,
	"ES256": true, "ES384": true, "ES512": true,
}

func loadJWTConfig(production bool) (jwtConfig, error) {
	cfg := jwtConfig{
		Mode:            strings.ToLower(strings.TrimSpace(env("JWT_MODE", jwtModeDisabled))),
		Issuer:          strings.TrimSpace(env("JWT_ISSUER", "")),
		JWKSURL:         strings.TrimSpace(env("JWT_JWKS_URL", "")),
		Skew:            60 * time.Second,
		JWKSCacheTTL:    10 * time.Minute,
		TenantIDClaim:   strings.TrimSpace(env("JWT_TENANT_ID_CLAIM", "tenant_id")),
		TenantSlugClaim: strings.TrimSpace(env("JWT_TENANT_SLUG_CLAIM", "tenant_slug")),
		AllowHTTP:       !production,
	}
	for _, audience := range strings.Split(env("JWT_AUDIENCE", ""), ",") {
		if audience = strings.TrimSpace(audience); audience != "" {
			cfg.Audiences = append(cfg.Audiences, audience)
		}
	}
	if raw := strings.TrimSpace(env("JWT_CLOCK_SKEW_SECONDS", "")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 0 || seconds > 300 {
			return cfg, errors.New("JWT_CLOCK_SKEW_SECONDS must be between 0 and 300")
		}
		cfg.Skew = time.Duration(seconds) * time.Second
	}
	if raw := strings.TrimSpace(env("JWT_JWKS_CACHE_SECONDS", "")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 30 || seconds > 86400 {
			return cfg, errors.New("JWT_JWKS_CACHE_SECONDS must be between 30 and 86400")
		}
		cfg.JWKSCacheTTL = time.Duration(seconds) * time.Second
	}

	switch cfg.Mode {
	case jwtModeDisabled:
		return cfg, nil
	case jwtModeJWKS:
		if cfg.Issuer == "" {
			return cfg, errors.New("JWT_ISSUER is required when JWT_MODE=jwks")
		}
		if len(cfg.Audiences) == 0 {
			return cfg, errors.New("JWT_AUDIENCE is required when JWT_MODE=jwks")
		}
		for _, algorithm := range strings.Split(env("JWT_ALLOWED_ALGS", "RS256,ES256"), ",") {
			algorithm = strings.TrimSpace(algorithm)
			if algorithm == "" {
				continue
			}
			if !asymmetricJWTAlgorithms[algorithm] {
				return cfg, fmt.Errorf("JWT_ALLOWED_ALGS: %q is not an accepted asymmetric algorithm", algorithm)
			}
			cfg.Algorithms = append(cfg.Algorithms, algorithm)
		}
		if len(cfg.Algorithms) == 0 {
			return cfg, errors.New("JWT_ALLOWED_ALGS must list at least one algorithm")
		}
		for name, raw := range map[string]string{"JWT_ISSUER": cfg.Issuer, "JWT_JWKS_URL": cfg.JWKSURL} {
			if raw == "" {
				continue
			}
			parsed, err := url.Parse(raw)
			if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(cfg.AllowHTTP && parsed.Scheme == "http")) {
				return cfg, fmt.Errorf("%s must be an https URL", name)
			}
		}
		return cfg, nil
	case jwtModeDevHS256:
		if production {
			return cfg, errors.New("JWT_MODE=dev-hs256 is not allowed when SUEZ_ENV=production")
		}
		cfg.DevSecret = []byte(env("JWT_DEV_SECRET", ""))
		if len(cfg.DevSecret) < 32 {
			return cfg, errors.New("JWT_DEV_SECRET must be at least 32 characters when JWT_MODE=dev-hs256")
		}
		cfg.Algorithms = []string{"HS256"}
		if cfg.Issuer == "" {
			cfg.Issuer = "suez-dev"
		}
		if len(cfg.Audiences) == 0 {
			cfg.Audiences = []string{"suez"}
		}
		return cfg, nil
	default:
		return cfg, errors.New("JWT_MODE must be disabled, jwks, or dev-hs256")
	}
}

type jwtVerifier struct {
	cfg  jwtConfig
	keys *jwksCache
}

func newJWTVerifier(cfg jwtConfig, client *http.Client) *jwtVerifier {
	verifier := &jwtVerifier{cfg: cfg}
	if cfg.Mode == jwtModeJWKS {
		verifier.keys = &jwksCache{cfg: cfg, client: client, now: time.Now}
	}
	return verifier
}

// verify checks signature, algorithm, issuer, audience, expiry and not-before.
// It returns errJWTInvalid for anything wrong with the token and
// errAuthenticationServiceUnavailable when signing keys cannot be obtained;
// in both cases the request is refused.
func (v *jwtVerifier) verify(ctx context.Context, raw string) (jwt.MapClaims, error) {
	if v == nil || v.cfg.Mode == jwtModeDisabled {
		return nil, fmt.Errorf("%w: token authentication is not enabled", errJWTInvalid)
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods(v.cfg.Algorithms),
		jwt.WithIssuer(v.cfg.Issuer),
		jwt.WithLeeway(v.cfg.Skew),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	claims := jwt.MapClaims{}
	var keyErr error
	_, err := parser.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if v.cfg.Mode == jwtModeDevHS256 {
			return v.cfg.DevSecret, nil
		}
		kid, _ := token.Header["kid"].(string)
		key, err := v.keys.key(ctx, kid, token.Method.Alg())
		if err != nil {
			keyErr = err
		}
		return key, err
	})
	if err != nil {
		if keyErr != nil && errors.Is(keyErr, errAuthenticationServiceUnavailable) {
			return nil, keyErr
		}
		return nil, fmt.Errorf("%w: %v", errJWTInvalid, err)
	}
	audiences, err := claims.GetAudience()
	if err != nil || !audienceMatches(audiences, v.cfg.Audiences) {
		return nil, fmt.Errorf("%w: audience is not accepted", errJWTInvalid)
	}
	return claims, nil
}

func audienceMatches(tokenAudiences, accepted []string) bool {
	for _, got := range tokenAudiences {
		for _, want := range accepted {
			if got == want {
				return true
			}
		}
	}
	return false
}

type jwksCache struct {
	cfg    jwtConfig
	client *http.Client
	now    func() time.Time

	mu          sync.Mutex
	keys        map[string]jwkKey
	fetchedAt   time.Time
	lastAttempt time.Time
	jwksURL     string
}

type jwkKey struct {
	public    any
	algorithm string
}

const jwksMinRefreshInterval = 30 * time.Second

// key returns the verification key for kid. Keys are cached for JWKSCacheTTL;
// an unknown kid triggers one refresh (rate limited) so key rotation is picked
// up without a restart. An expired cache that cannot be refreshed is an error:
// stale keys are never used.
func (c *jwksCache) key(ctx context.Context, kid, algorithm string) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	fresh := c.keys != nil && c.now().Sub(c.fetchedAt) < c.cfg.JWKSCacheTTL
	if fresh {
		if key, ok := c.lookup(kid, algorithm); ok {
			return key, nil
		}
		if c.now().Sub(c.lastAttempt) < jwksMinRefreshInterval {
			return nil, errors.New("no matching signing key")
		}
	}
	if err := c.refresh(ctx); err != nil {
		if fresh {
			return nil, errors.New("no matching signing key")
		}
		c.keys = nil
		return nil, fmt.Errorf("%w: signing keys unavailable: %v", errAuthenticationServiceUnavailable, err)
	}
	if key, ok := c.lookup(kid, algorithm); ok {
		return key, nil
	}
	return nil, errors.New("no matching signing key")
}

func (c *jwksCache) lookup(kid, algorithm string) (any, bool) {
	if kid == "" {
		// Without a kid the choice is only unambiguous when there is one key.
		if len(c.keys) != 1 {
			return nil, false
		}
		for _, key := range c.keys {
			return key.public, keyFitsAlgorithm(key, algorithm)
		}
	}
	key, ok := c.keys[kid]
	if !ok || !keyFitsAlgorithm(key, algorithm) {
		return nil, false
	}
	return key.public, true
}

func keyFitsAlgorithm(key jwkKey, algorithm string) bool {
	if key.algorithm != "" && key.algorithm != algorithm {
		return false
	}
	switch key.public.(type) {
	case *rsa.PublicKey:
		return strings.HasPrefix(algorithm, "RS") || strings.HasPrefix(algorithm, "PS")
	case *ecdsa.PublicKey:
		return strings.HasPrefix(algorithm, "ES")
	}
	return false
}

func (c *jwksCache) refresh(ctx context.Context) error {
	c.lastAttempt = c.now()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if c.jwksURL == "" {
		c.jwksURL = c.cfg.JWKSURL
	}
	if c.jwksURL == "" {
		discovered, err := c.discover(ctx)
		if err != nil {
			return err
		}
		c.jwksURL = discovered
	}
	var document struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := c.getJSON(ctx, c.jwksURL, &document); err != nil {
		return err
	}
	keys := map[string]jwkKey{}
	for index, raw := range document.Keys {
		if use, _ := raw["use"].(string); use != "" && use != "sig" {
			continue
		}
		public, err := parseJWK(raw)
		if err != nil {
			continue
		}
		kid, _ := raw["kid"].(string)
		if kid == "" {
			kid = fmt.Sprintf("\x00unnamed-%d", index)
		}
		algorithm, _ := raw["alg"].(string)
		keys[kid] = jwkKey{public: public, algorithm: algorithm}
	}
	if len(keys) == 0 {
		return errors.New("jwks contains no usable signing keys")
	}
	c.keys = keys
	c.fetchedAt = c.now()
	return nil
}

func (c *jwksCache) discover(ctx context.Context) (string, error) {
	var metadata struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	endpoint := strings.TrimRight(c.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	if err := c.getJSON(ctx, endpoint, &metadata); err != nil {
		return "", err
	}
	if metadata.Issuer != c.cfg.Issuer {
		return "", errors.New("openid configuration issuer does not match JWT_ISSUER")
	}
	parsed, err := url.Parse(metadata.JWKSURI)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(c.cfg.AllowHTTP && parsed.Scheme == "http")) {
		return "", errors.New("openid configuration has no usable jwks_uri")
	}
	return metadata.JWKSURI, nil
}

func (c *jwksCache) getJSON(ctx context.Context, endpoint string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", endpoint, response.Status)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out)
}

func parseJWK(raw map[string]any) (any, error) {
	field := func(name string) (*big.Int, error) {
		value, _ := raw[name].(string)
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
		if err != nil || len(decoded) == 0 {
			return nil, fmt.Errorf("jwk field %s is missing or malformed", name)
		}
		return new(big.Int).SetBytes(decoded), nil
	}
	switch raw["kty"] {
	case "RSA":
		n, err := field("n")
		if err != nil {
			return nil, err
		}
		e, err := field("e")
		if err != nil {
			return nil, err
		}
		if n.BitLen() < 2048 || !e.IsInt64() || e.Int64() < 3 || e.Int64() > 1<<31-1 {
			return nil, errors.New("rsa key is too small or has an invalid exponent")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		var curve elliptic.Curve
		switch raw["crv"] {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, errors.New("unsupported curve")
		}
		x, err := field("x")
		if err != nil {
			return nil, err
		}
		y, err := field("y")
		if err != nil {
			return nil, err
		}
		if !curve.IsOnCurve(x, y) {
			return nil, errors.New("ec point is not on the curve")
		}
		return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
	}
	return nil, errors.New("unsupported key type")
}
