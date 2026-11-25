package outpost

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// SameOrigin refuses state-changing requests a browser sent from another site.
//
// Behind the gateway every app is on the same site, so the browser attaches the
// user's login to a request one app's page makes to another, and the gateway
// adds an identity to it (spec §3, "Check the origin of your own forms"). The
// reader itself sends no Origin or Referer at all, so a request carrying
// neither is let through: that is a non-browser client, which a cross-site
// form cannot impersonate. Only a header that names a different host is
// refused.
//
// Applied whether or not Outpost is present; no legitimate client of shelf
// sends a foreign Origin.
func SameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		source := r.Header.Get("Origin")
		if source == "" {
			source = r.Header.Get("Referer")
		}
		if source != "" && !fromHost(source, requestHosts(r)) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// fromHost reports whether an Origin or Referer value names one of hosts. The
// literal "null" origin (sandboxed frames, file: pages) names nothing and is
// refused.
func fromHost(source string, hosts []string) bool {
	u, err := url.Parse(source)
	if err != nil || u.Host == "" {
		return false
	}
	// Unlike the token audience, the port counts here: a page on another port
	// of the same machine is a different origin, and on a home network that
	// is exactly where some other web app would be.
	src := withDefaultPort(u.Host, u.Scheme)
	for _, h := range hosts {
		if strings.EqualFold(src, withDefaultPort(h, u.Scheme)) {
			return true
		}
	}
	return false
}

// withDefaultPort makes an implicit port explicit, since Origin omits the
// scheme's default port and Host may or may not carry it.
func withDefaultPort(host, scheme string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	port := "80"
	if scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), port)
}
