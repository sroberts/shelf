package outpost

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseEnv(t *testing.T) {
	env := map[string]string{
		EnvStorageDir: "/srv/shelf",
		EnvPort:       "8091",
		EnvAuth:       "gateway",
		EnvIssuer:     "https://portal.example",
		EnvJWKSURL:    "http://127.0.0.1:1/jwks",
	}
	e := parse(func(k string) string { return env[k] })
	if !e.Supervised() || e.Port != 8091 || !e.Gateway || e.Issuer == "" || e.JWKSURL == "" {
		t.Errorf("got %+v", e)
	}

	for _, bad := range []string{"", "0", "-1", "65536", "eighty"} {
		if p := parse(func(k string) string {
			if k == EnvPort {
				return bad
			}
			return ""
		}).Port; p != 0 {
			t.Errorf("OUTPOST_PORT=%q parsed as %d, want 0", bad, p)
		}
	}

	// Only the exact value means the gateway is in front; anything else and
	// the X-Outpost-* headers may have come from anyone.
	if parse(func(k string) string {
		if k == EnvAuth {
			return "Gateway"
		}
		return ""
	}).Gateway {
		t.Error("OUTPOST_AUTH=Gateway must not enable gateway mode")
	}
}

func TestNoVerifierWithoutGateway(t *testing.T) {
	if v := NewVerifier(Env{Issuer: "i", JWKSURL: "u"}); v != nil {
		t.Error("a verifier without OUTPOST_AUTH=gateway would trust unstripped headers")
	}
	if v := NewVerifier(Env{Gateway: true}); v != nil {
		t.Error("a verifier without issuer and JWKS cannot verify anything")
	}
}

// --- identity ---

type testGateway struct {
	t       *testing.T
	priv    ed25519.PrivateKey
	kid     string
	fetches atomic.Int32
	srv     *httptest.Server
}

const testIssuer = "https://portal.tailnet.ts.net"

func newGateway(t *testing.T) *testGateway {
	t.Helper()
	g := &testGateway{t: t, kid: "k1"}
	_, g.priv, _ = ed25519.GenerateKey(rand.Reader)
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		g.fetches.Add(1)
		pub := g.priv.Public().(ed25519.PublicKey)
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "OKP", "crv": "Ed25519", "kid": g.kid, "use": "sig",
			"x": base64.RawURLEncoding.EncodeToString(pub),
		}}})
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *testGateway) verifier() *Verifier {
	return NewVerifier(Env{Gateway: true, Issuer: testIssuer, JWKSURL: g.srv.URL})
}

func (g *testGateway) sign(header, claims map[string]any) string {
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			g.t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(header) + "." + enc(claims)
	sig := ed25519.Sign(g.priv, []byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (g *testGateway) token(mod func(h, c map[string]any)) string {
	h := map[string]any{"alg": "EdDSA", "typ": "JWT", "kid": g.kid}
	c := map[string]any{
		"iss": testIssuer, "aud": "shelf.tailnet.ts.net", "sub": "ann",
		"name": "Ann", "email": "ann@example.com", "groups": []string{"admins"},
		"iat": time.Now().Unix(), "exp": time.Now().Add(60 * time.Second).Unix(),
	}
	if mod != nil {
		mod(h, c)
	}
	return g.sign(h, c)
}

func gatewayRequest(token string) *http.Request {
	r := httptest.NewRequest("GET", "http://shelf.tailnet.ts.net/opds", nil)
	r.Host = "shelf.tailnet.ts.net"
	r.Header.Set("X-Forwarded-Host", "shelf.tailnet.ts.net")
	if token != "" {
		r.Header.Set(IdentityHeader, token)
	}
	return r
}

func TestVerifyAcceptsAGatewayToken(t *testing.T) {
	g := newGateway(t)
	id, err := g.verifier().VerifyRequest(gatewayRequest(g.token(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if id.User != "ann" || id.Name != "Ann" || id.Email != "ann@example.com" ||
		len(id.Groups) != 1 || id.Groups[0] != "admins" {
		t.Errorf("got %+v", id)
	}
}

func TestVerifyRejects(t *testing.T) {
	g := newGateway(t)
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)

	cases := []struct {
		name  string
		token string
		want  error
	}{
		{"no token", "", ErrNoToken},
		{"garbage", "not.a.jwt.at.all", ErrMalformed},
		{"alg none", g.token(func(h, _ map[string]any) { h["alg"] = "none" }), ErrAlgorithm},
		{"alg HS256", g.token(func(h, _ map[string]any) { h["alg"] = "HS256" }), ErrAlgorithm},
		{"expired", g.token(func(_, c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }), ErrExpired},
		{"no exp", g.token(func(_, c map[string]any) { delete(c, "exp") }), ErrExpired},
		{"not yet valid", g.token(func(_, c map[string]any) { c["nbf"] = time.Now().Add(time.Minute).Unix() }), ErrExpired},
		{"wrong issuer", g.token(func(_, c map[string]any) { c["iss"] = "https://evil" }), ErrIssuer},
		// A token minted for another app behind the same gateway must not
		// open this one: that is what aud is for.
		{"other app's audience", g.token(func(_, c map[string]any) { c["aud"] = "yarr.tailnet.ts.net" }), ErrAudience},
		{"no subject", g.token(func(_, c map[string]any) { c["sub"] = "" }), ErrNoSubject},
		{"tampered claims", tamper(g.token(nil)), ErrSignature},
		{"signed by another key", func() string {
			saved := g.priv
			g.priv = otherKey
			defer func() { g.priv = saved }()
			return g.token(nil)
		}(), ErrSignature},
	}
	v := g.verifier()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.VerifyRequest(gatewayRequest(tc.token))
			if !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// tamper rewrites the payload to claim a different user, keeping the
// original signature.
func tamper(token string) string {
	parts := strings.Split(token, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c map[string]any
	json.Unmarshal(raw, &c)
	c["sub"] = "mallory"
	b, _ := json.Marshal(c)
	parts[1] = base64.RawURLEncoding.EncodeToString(b)
	return strings.Join(parts, ".")
}

func TestVerifyAudienceForms(t *testing.T) {
	g := newGateway(t)
	v := g.verifier()
	for name, aud := range map[string]any{
		"array":          []string{"other", "shelf.tailnet.ts.net"},
		"different case": "Shelf.Tailnet.TS.net",
		"with port":      "shelf.tailnet.ts.net:443",
	} {
		t.Run(name, func(t *testing.T) {
			tok := g.token(func(_, c map[string]any) { c["aud"] = aud })
			if _, err := v.VerifyRequest(gatewayRequest(tok)); err != nil {
				t.Error(err)
			}
		})
	}
}

// The key set is fetched once and reused; a rotated key is picked up on the
// next unknown kid, but junk kids cannot force a fetch per request.
func TestJWKSFetchIsCachedAndRateLimited(t *testing.T) {
	g := newGateway(t)
	v := g.verifier()
	now := time.Now()
	v.now = func() time.Time { return now }

	for range 5 {
		if _, err := v.VerifyRequest(gatewayRequest(g.token(nil))); err != nil {
			t.Fatal(err)
		}
	}
	if n := g.fetches.Load(); n != 1 {
		t.Fatalf("%d JWKS fetches for one key, want 1", n)
	}

	junk := g.token(func(h, _ map[string]any) { h["kid"] = "junk" })
	for range 5 {
		if _, err := v.VerifyRequest(gatewayRequest(junk)); !errors.Is(err, ErrUnknownKey) {
			t.Fatalf("got %v, want ErrUnknownKey", err)
		}
	}
	if n := g.fetches.Load(); n != 1 {
		t.Fatalf("unknown kids caused %d fetches inside the interval, want 1", n)
	}

	// The gateway rotates; once the interval has passed the new key works.
	_, g.priv, _ = ed25519.GenerateKey(rand.Reader)
	g.kid = "k2"
	now = now.Add(refetchInterval + time.Second)
	if _, err := v.VerifyRequest(gatewayRequest(g.token(nil))); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
}

func TestVerifyHonoursContext(t *testing.T) {
	g := newGateway(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v := g.verifier()
	if _, err := v.Verify(ctx, g.token(nil), []string{"shelf.tailnet.ts.net"}); err == nil {
		t.Error("a cancelled request should not complete a JWKS fetch")
	}
	// One client hanging up must not stop the next request verifying.
	if _, err := v.Verify(context.Background(), g.token(nil), []string{"shelf.tailnet.ts.net"}); err != nil {
		t.Errorf("after a cancelled fetch: %v", err)
	}
}

// --- origin ---

func TestSameOrigin(t *testing.T) {
	h := SameOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	cases := []struct {
		name    string
		method  string
		host    string
		fwdHost string
		origin  string
		referer string
		want    int
	}{
		// The reader sends neither header, and must keep working.
		{"reader, no origin", "PUT", "192.168.1.5:8080", "", "", "", http.StatusNoContent},
		{"same origin, LAN", "POST", "192.168.1.5:8080", "", "http://192.168.1.5:8080", "", http.StatusNoContent},
		{"same origin, gateway", "POST", "shelf.tailnet.ts.net", "shelf.tailnet.ts.net", "https://shelf.tailnet.ts.net", "", http.StatusNoContent},
		{"same origin via referer", "POST", "shelf.tailnet.ts.net", "", "", "https://shelf.tailnet.ts.net/opds", http.StatusNoContent},
		{"another app on the gateway", "POST", "shelf.tailnet.ts.net", "shelf.tailnet.ts.net", "https://yarr.tailnet.ts.net", "", http.StatusForbidden},
		{"another port, same machine", "PUT", "192.168.1.5:8080", "", "http://192.168.1.5:3000", "", http.StatusForbidden},
		{"null origin", "POST", "192.168.1.5:8080", "", "null", "", http.StatusForbidden},
		{"cross-site referer", "POST", "192.168.1.5:8080", "", "", "https://evil.example/x", http.StatusForbidden},
		{"GET is never checked", "GET", "192.168.1.5:8080", "", "https://evil.example", "", http.StatusNoContent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://"+tc.host+"/users/create", nil)
			r.Host = tc.host
			if tc.fwdHost != "" {
				r.Header.Set("X-Forwarded-Host", tc.fwdHost)
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.referer != "" {
				r.Header.Set("Referer", tc.referer)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Errorf("got %d, want %d", w.Code, tc.want)
			}
		})
	}
}
