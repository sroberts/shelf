package main

import (
	"errors"
	"testing"

	"github.com/sroberts/shelf/internal/outpost"
)

func TestListenAddr(t *testing.T) {
	supervised := outpost.Env{StorageDir: "/srv/shelf", Port: 8091}

	cases := []struct {
		name       string
		listen     string
		legacy     string
		port       int
		env        outpost.Env
		configured string
		want       string
		wantErr    bool
	}{
		{name: "standalone default", want: ":8080"},
		{name: "standalone config", configured: "0.0.0.0:9000", want: "0.0.0.0:9000"},
		{name: "--listen wins", listen: ":7000", port: 1234, env: supervised, want: ":7000"},
		{name: "legacy --kosync", legacy: ":7001", want: ":7001"},
		// --port is loopback even standalone; the LAN is --listen's job.
		{name: "--port binds loopback", port: 1234, configured: ":9000", want: "127.0.0.1:1234"},
		{name: "--port beats OUTPOST_PORT", port: 1234, env: supervised, want: "127.0.0.1:1234"},
		// The config's default ":8080" binds every interface; under Outpost
		// it must never be reached for.
		{name: "OUTPOST_PORT beats config", env: supervised, configured: ":8080", want: "127.0.0.1:8091"},
		{name: "supervised without a port", env: outpost.Env{StorageDir: "/srv/shelf"}, configured: ":8080", wantErr: true},
		{name: "port out of range", port: 70000, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := listenAddr(tc.listen, tc.legacy, tc.port, tc.env, tc.configured)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	if _, err := listenAddr("", "", -1, outpost.Env{}, ""); !errors.Is(err, errUsage) {
		t.Errorf("a bad --port should be a usage error, got %v", err)
	}
}
