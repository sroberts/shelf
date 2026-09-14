package target

import (
	"context"
	"testing"

	"github.com/sroberts/shelf/internal/config"
)

func TestLabel(t *testing.T) {
	tests := []struct {
		name string
		dev  config.Device
		want string
	}{
		{
			name: "sdcard with mount",
			dev:  config.Device{Transport: config.TransportSD, Mount: "/Volumes/CARD"},
			want: "/Volumes/CARD",
		},
		{
			name: "sdcard without mount",
			dev:  config.Device{Transport: config.TransportSD, Mount: ""},
			want: "(no mount configured)",
		},
		{
			name: "network device with host",
			dev:  config.Device{Transport: config.TransportWS, Host: "crosspoint.local"},
			want: "crosspoint.local",
		},
		{
			name: "network device without host",
			dev:  config.Device{Transport: config.TransportWS, Host: ""},
			want: "(discover)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Label(tt.dev)
			if got != tt.want {
				t.Errorf("Label() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		name string
		dev  config.Device
		want string
	}{
		{
			name: "sdcard",
			dev:  config.Device{Transport: config.TransportSD, Mount: "/Volumes/CARD"},
			want: "SD card at /Volumes/CARD",
		},
		{
			name: "network device",
			dev:  config.Device{Transport: config.TransportWS, Host: "crosspoint.local"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Describe(tt.dev)
			if got != tt.want {
				t.Errorf("Describe() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNetworkClientWithHost(t *testing.T) {
	dev := config.Device{
		Host:      "192.168.1.50",
		Transport: config.TransportWS,
	}

	client, err := NetworkClient(context.Background(), dev)
	if err != nil {
		t.Fatalf("NetworkClient() unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("NetworkClient() returned nil client")
	}
	if client.Host() != "192.168.1.50" {
		t.Errorf("client.Host() = %q, want %q", client.Host(), "192.168.1.50")
	}
}

func TestOpen(t *testing.T) {
	ctx := context.Background()

	t.Run("network device with host", func(t *testing.T) {
		dev := config.Device{
			Host:      "10.0.0.42",
			Transport: config.TransportHTTP,
		}
		tgt, label, err := Open(ctx, dev, "")
		if err != nil {
			t.Fatalf("Open() unexpected error: %v", err)
		}
		if tgt == nil {
			t.Fatal("Open() returned nil Target")
		}
		if label != "10.0.0.42" {
			t.Errorf("Open() label = %q, want %q", label, "10.0.0.42")
		}
	})

	t.Run("sdcard valid mount", func(t *testing.T) {
		tempMount := t.TempDir()
		dev := config.Device{
			Transport: config.TransportSD,
			Mount:     tempMount,
		}
		tgt, label, err := Open(ctx, dev, "")
		if err != nil {
			t.Fatalf("Open() unexpected error on valid mount: %v", err)
		}
		if tgt == nil {
			t.Fatal("Open() returned nil Target")
		}
		if label != tempMount {
			t.Errorf("Open() label = %q, want %q", label, tempMount)
		}
	})

	t.Run("sdcard nonexistent mount", func(t *testing.T) {
		dev := config.Device{
			Transport: config.TransportSD,
			Mount:     "/nonexistent/directory/shelf/test",
		}
		_, _, err := Open(ctx, dev, "")
		if err == nil {
			t.Fatal("Open() expected error on nonexistent mount, got nil")
		}
	})
}
