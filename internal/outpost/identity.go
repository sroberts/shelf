package outpost

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// IdentityHeader carries the gateway's signed identity token (spec §3).
const IdentityHeader = "X-Outpost-Identity"

// Identity is the verified caller.
type Identity struct {
	User   string
	Name   string
	Email  string
	Groups []string
}

// Verification failures. Callers treat every one the same way — the request is
// not pre-authenticated — but the distinction is worth having in a log line.
var (
	ErrNoToken       = errors.New("no identity token")
	ErrMalformed     = errors.New("malformed identity token")
	ErrAlgorithm     = errors.New("identity token is not EdDSA")
	ErrSignature     = errors.New("identity token signature does not verify")
	ErrExpired       = errors.New("identity token expired")
	ErrIssuer        = errors.New("identity token issuer mismatch")
	ErrAudience      = errors.New("identity token audience mismatch")
	ErrNoSubject     = errors.New("identity token has no subject")
	ErrUnknownKey    = errors.New("identity token signed by an unknown key")
	errNotConfigured = errors.New("identity verification is not configured")
)

// Verifier checks X-Outpost-Identity against the gateway's published keys.
//
// The plain X-Outpost-User header is not enough for shelf: the catalog hands
// out the whole library, and any other process on the machine can reach the
// loopback port directly and claim to be anyone. The signed token is the one
// thing such a neighbour cannot forge (spec §3).
//
// Safe for concurrent use: it is shared by every request handler. That is a
// plain net/http handler, not a Bubble Tea model, so a mutex is the right tool.
type Verifier struct {
	Issuer  string
	JWKSURL string
	Client  *http.Client

	// Leeway absorbs clock skew between the gateway and this process. The
	// token lives 60 seconds, so this is kept small.
	Leeway time.Duration

	now func() time.Time

	mu      sync.Mutex
	keys    map[string]ed25519.PublicKey
	fetched time.Time
}

// refetchInterval bounds how often an unknown key id triggers a JWKS fetch,
// so a client spraying junk tokens cannot turn every request into one.
const refetchInterval = 30 * time.Second

// NewVerifier builds a verifier from Outpost's environment, or returns nil
// when the gateway is off — in which case no X-Outpost-* header can be
// trusted at all (spec §3: nothing strips them then).
func NewVerifier(e Env) *Verifier {
	if !e.Gateway || e.Issuer == "" || e.JWKSURL == "" {
		return nil
	}
	return &Verifier{
		Issuer:  e.Issuer,
		JWKSURL: e.JWKSURL,
		Client:  &http.Client{Timeout: 5 * time.Second},
		Leeway:  5 * time.Second,
	}
}

// VerifyRequest verifies the identity token on r. The audience is the host the
// request was served at, which the gateway preserves in Host and repeats in
// X-Forwarded-Host.
func (v *Verifier) VerifyRequest(r *http.Request) (Identity, error) {
	if v == nil {
		return Identity{}, errNotConfigured
	}
	token := r.Header.Get(IdentityHeader)
	if token == "" {
		return Identity{}, ErrNoToken
	}
	return v.Verify(r.Context(), token, requestHosts(r))
}

// requestHosts lists the names this request may have been addressed to.
func requestHosts(r *http.Request) []string {
	var hosts []string
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
		// A proxy chain appends; the first entry is what the client asked for.
		first, _, _ := strings.Cut(fh, ",")
		hosts = append(hosts, strings.TrimSpace(first))
	}
	if r.Host != "" {
		hosts = append(hosts, r.Host)
	}
	return hosts
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type jwtClaims struct {
	Iss    string          `json:"iss"`
	Sub    string          `json:"sub"`
	Aud    json.RawMessage `json:"aud"`
	Exp    *float64        `json:"exp"`
	Nbf    *float64        `json:"nbf"`
	Name   string          `json:"name"`
	Email  string          `json:"email"`
	Groups json.RawMessage `json:"groups"`
}

// Verify checks a compact JWS and its claims.
func (v *Verifier) Verify(ctx context.Context, token string, audiences []string) (Identity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Identity{}, ErrMalformed
	}

	var hdr jwtHeader
	if err := decodeSegment(parts[0], &hdr); err != nil {
		return Identity{}, ErrMalformed
	}
	// Pinned rather than read from the header: honouring whatever alg the
	// token names is the classic JWT downgrade.
	if hdr.Alg != "EdDSA" {
		return Identity{}, ErrAlgorithm
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return Identity{}, ErrMalformed
	}
	signed := []byte(parts[0] + "." + parts[1])

	keys, err := v.keysFor(ctx, hdr.Kid)
	if err != nil {
		return Identity{}, err
	}
	ok := false
	for _, k := range keys {
		if ed25519.Verify(k, signed, sig) {
			ok = true
			break
		}
	}
	if !ok {
		return Identity{}, ErrSignature
	}

	var c jwtClaims
	if err := decodeSegment(parts[1], &c); err != nil {
		return Identity{}, ErrMalformed
	}

	now := v.clock()
	if c.Exp == nil || now.After(unix(*c.Exp).Add(v.Leeway)) {
		return Identity{}, ErrExpired
	}
	if c.Nbf != nil && now.Add(v.Leeway).Before(unix(*c.Nbf)) {
		return Identity{}, ErrExpired
	}
	if c.Iss != v.Issuer {
		return Identity{}, ErrIssuer
	}
	if !audienceMatches(c.Aud, audiences) {
		return Identity{}, ErrAudience
	}
	if c.Sub == "" {
		return Identity{}, ErrNoSubject
	}

	return Identity{
		User:   c.Sub,
		Name:   c.Name,
		Email:  c.Email,
		Groups: stringList(c.Groups),
	}, nil
}

func (v *Verifier) clock() time.Time {
	if v.now != nil {
		return v.now()
	}
	return time.Now()
}

func unix(f float64) time.Time { return time.Unix(int64(f), 0) }

func decodeSegment(seg string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// audienceMatches accepts aud as a string or an array, and compares hosts with
// and without a port, case-insensitively: DNS names are not case-sensitive,
// and whether a port survives into Host depends on the client.
func audienceMatches(raw json.RawMessage, hosts []string) bool {
	auds := stringList(raw)
	for _, a := range auds {
		for _, h := range hosts {
			if sameHost(a, h) {
				return true
			}
		}
	}
	return false
}

func sameHost(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return stripPort(a) == stripPort(b)
}

func stripPort(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// stringList reads a JSON string, array of strings, or comma-separated string.
func stringList(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// keysFor returns the candidate keys for a key id, fetching the JWKS when the
// id is not yet known (the gateway rotated) or nothing has been fetched.
func (v *Verifier) keysFor(ctx context.Context, kid string) ([]ed25519.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if pick := v.pick(kid); len(pick) > 0 {
		return pick, nil
	}
	if !v.fetched.IsZero() && v.clock().Sub(v.fetched) < refetchInterval {
		return nil, ErrUnknownKey
	}

	keys, err := v.fetch(ctx)
	if err != nil {
		// Stamped on failure too, so an unreachable JWKS endpoint is retried
		// at most every refetchInterval rather than on every request — unless
		// the failure was this request's own client hanging up, which says
		// nothing about the endpoint and must not lock out everyone else.
		if ctx.Err() == nil {
			v.fetched = v.clock()
		}
		return nil, err
	}
	v.fetched = v.clock()
	v.keys = keys

	if pick := v.pick(kid); len(pick) > 0 {
		return pick, nil
	}
	return nil, ErrUnknownKey
}

// pick selects keys by id; a token without a kid is tried against every key.
func (v *Verifier) pick(kid string) []ed25519.PublicKey {
	if kid != "" {
		if k, ok := v.keys[kid]; ok {
			return []ed25519.PublicKey{k}
		}
		return nil
	}
	out := make([]ed25519.PublicKey, 0, len(v.keys))
	for _, k := range v.keys {
		out = append(out, k)
	}
	return out
}

type jwks struct {
	Keys []struct {
		Kty string `json:"kty"`
		Crv string `json:"crv"`
		Kid string `json:"kid"`
		X   string `json:"x"`
		Use string `json:"use"`
	} `json:"keys"`
}

func (v *Verifier) fetch(ctx context.Context) (map[string]ed25519.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.JWKSURL, nil)
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	client := v.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: %s", resp.Status)
	}

	var set jwks
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}

	keys := make(map[string]ed25519.PublicKey, len(set.Keys))
	for i, k := range set.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil || len(x) != ed25519.PublicKeySize {
			continue
		}
		id := k.Kid
		if id == "" {
			// Unnamed keys still need distinct map slots to be tried.
			id = fmt.Sprintf("\x00%d", i)
		}
		keys[id] = ed25519.PublicKey(x)
	}
	if len(keys) == 0 {
		return nil, errors.New("jwks: no Ed25519 keys")
	}
	return keys, nil
}
