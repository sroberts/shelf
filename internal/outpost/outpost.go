// Package outpost is what shelf needs to run as a service supervised by
// Outpost: reading the environment it is started with, verifying the identity
// token its login gateway attaches, and refusing cross-site writes.
//
// The contract is outpost-service-spec.md, revision v0.9.4. Everything here
// exists because a section of it asks for it; the section is named beside each
// piece so the two can be diffed when the contract moves.
package outpost

import (
	"os"
	"strconv"
	"strings"
)

// Environment variables Outpost injects (spec §3). OUTPOST_PORT is not among
// what the runtime itself sets, but every manifest `outpost add` generates
// carries it (spec §1, §14), so it is read here alongside the others.
const (
	EnvStorageDir = "OUTPOST_STORAGE_DIR"
	EnvPort       = "OUTPOST_PORT"
	EnvAuth       = "OUTPOST_AUTH"
	EnvIssuer     = "OUTPOST_ISSUER"
	EnvJWKSURL    = "OUTPOST_JWKS_URL"
)

// Env is what Outpost told this process about itself.
type Env struct {
	// StorageDir is the one directory backed up and restored (spec §4). Its
	// presence is what marks the process as supervised.
	StorageDir string
	// Port is OUTPOST_PORT, or zero when unset or not a number.
	Port int
	// Gateway is true when Outpost's login gateway authenticates every
	// request before it arrives (spec §3, "Behind the login gateway").
	Gateway bool
	Issuer  string
	JWKSURL string
}

// FromEnv reads the process environment.
func FromEnv() Env { return parse(os.Getenv) }

func parse(get func(string) string) Env {
	e := Env{
		StorageDir: strings.TrimSpace(get(EnvStorageDir)),
		Gateway:    get(EnvAuth) == "gateway",
		Issuer:     get(EnvIssuer),
		JWKSURL:    get(EnvJWKSURL),
	}
	if p, err := strconv.Atoi(strings.TrimSpace(get(EnvPort))); err == nil && p > 0 && p < 65536 {
		e.Port = p
	}
	return e
}

// Supervised reports whether Outpost started this process.
//
// Keyed on the storage directory rather than the port: the port is something
// a hand-written manifest may leave out, but Outpost always sets storage.
func (e Env) Supervised() bool { return e.StorageDir != "" }
