package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/sroberts/shelf/internal/config"
	"github.com/sroberts/shelf/internal/device"
)

var cmdDevices = &command{
	name:    "devices",
	summary: "list configured devices and discover them on the network",
	usage:   "devices [--discover] [--timeout D] [--json]",
	run: func(ctx context.Context, a *app, args []string) error {
		fs := newFlagSet("devices")
		discover := fs.Bool("discover", false, "broadcast a discovery request on the local network")
		timeout := fs.Duration("timeout", 3*time.Second, "discovery timeout")
		asJSON := fs.Bool("json", false, "emit newline-delimited JSON")
		if err := parseFlags(fs, args); err != nil {
			return err
		}

		cfg, err := a.config()
		if err != nil {
			return err
		}

		if *discover {
			return discoverDevices(ctx, *timeout, *asJSON)
		}
		return listConfiguredDevices(ctx, cfg, *asJSON)
	},
}

func discoverDevices(ctx context.Context, timeout time.Duration, asJSON bool) error {
	found, err := device.Discover(ctx, timeout)
	if err != nil {
		return err
	}

	if len(found) == 0 {
		fmt.Fprintf(os.Stderr,
			"no devices answered on UDP %d within %s\n"+
				"check that the device is awake and in File Transfer or Calibre Wireless mode\n",
			device.DiscoveryPort, timeout)
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if !asJSON {
		fmt.Fprintln(w, "ADDRESS\tHOSTNAME\tMODEL\tFIRMWARE\tMODE\tRSSI\tFREE HEAP")
	}

	for _, d := range found {
		// Discovery only reports an address; poll for the details.
		c := device.New(d.Host())
		status, statusErr := c.Status(ctx)

		if asJSON {
			row := struct {
				Address  string `json:"address"`
				Hostname string `json:"hostname"`
				WSPort   int    `json:"ws_port"`
				Model    string `json:"model,omitempty"`
				Firmware string `json:"firmware,omitempty"`
				Mode     string `json:"mode,omitempty"`
				Error    string `json:"error,omitempty"`
			}{Address: d.Addr, Hostname: d.Hostname, WSPort: d.WSPort}
			if statusErr != nil {
				row.Error = statusErr.Error()
			} else {
				row.Model, row.Firmware, row.Mode = status.Device, status.Version, status.Mode
			}
			if err := emitJSON(row); err != nil {
				return err
			}
			continue
		}

		if statusErr != nil {
			fmt.Fprintf(w, "%s\t%s\t-\t-\t-\t-\t(%v)\n", d.Addr, d.Hostname, statusErr)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d dBm\t%s\n",
			d.Addr, d.Hostname, status.Device, status.Version, status.Mode,
			status.RSSI, humanSize(status.FreeHeap))
	}

	if !asJSON {
		return w.Flush()
	}
	return nil
}

func listConfiguredDevices(ctx context.Context, cfg *config.Config, asJSON bool) error {
	if len(cfg.Devices) == 0 {
		fmt.Fprintln(os.Stderr,
			"no devices configured; add a [[device]] block to config.toml, "+
				"or run 'shelf devices --discover'")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if !asJSON {
		fmt.Fprintln(w, "NICKNAME\tHOST\tTRANSPORT\tROOT\tSTATUS")
	}

	for _, d := range cfg.Devices {
		host := d.Host
		state := "ok"

		var status *device.Status
		var statusErr error

		if d.Transport == config.TransportSD {
			state = "sd: " + d.Mount
			host = d.Mount
		} else {
			c, err := deviceClient(ctx, d)
			if err != nil {
				statusErr = err
			} else {
				host = c.Host()
				status, statusErr = c.Status(ctx)
			}
			switch {
			case errors.Is(statusErr, device.ErrNotInTransfer):
				state = "asleep or not in transfer mode"
			case statusErr != nil:
				state = statusErr.Error()
			default:
				state = fmt.Sprintf("%s %s, %s free heap",
					status.Device, status.Version, humanSize(status.FreeHeap))
			}
		}

		if asJSON {
			row := struct {
				Nickname  string `json:"nickname"`
				Host      string `json:"host"`
				Transport string `json:"transport"`
				Root      string `json:"root"`
				Reachable bool   `json:"reachable"`
				Model     string `json:"model,omitempty"`
				Firmware  string `json:"firmware,omitempty"`
				Status    string `json:"status"`
			}{
				Nickname: d.Nickname, Host: host, Transport: string(d.Transport),
				Root: d.Root, Reachable: statusErr == nil, Status: state,
			}
			if status != nil {
				row.Model, row.Firmware = status.Device, status.Version
			}
			if err := emitJSON(row); err != nil {
				return err
			}
			continue
		}

		if host == "" {
			host = "(discovery)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.Nickname, host, d.Transport, d.Root, state)
	}

	if !asJSON {
		return w.Flush()
	}
	return nil
}

// deviceClient builds a client for a configured device, discovering it when no
// host is set.
func deviceClient(ctx context.Context, d config.Device) (*device.Client, error) {
	if d.Host != "" {
		return device.New(d.Host), nil
	}
	return device.Resolve(ctx, "")
}

// checkDevice reports on one device for `shelf doctor`.
func checkDevice(ctx context.Context, w *tabwriter.Writer, d config.Device) {
	if d.Transport == config.TransportSD {
		fmt.Fprintf(w, "device %s\tSD transport at %s%s\n", d.Nickname, d.Mount, existsNote(d.Mount))
		return
	}

	c, err := deviceClient(ctx, d)
	if err != nil {
		fmt.Fprintf(w, "device %s\tunreachable: %v\n", d.Nickname, err)
		return
	}

	status, err := c.Status(ctx)
	if err != nil {
		fmt.Fprintf(w, "device %s\t%v\n", d.Nickname, err)
		return
	}

	fmt.Fprintf(w, "device %s\t%s %s at %s\n", d.Nickname, status.Device, status.Version, c.Host())
	fmt.Fprintf(w, "  mode\t%s, rssi %d dBm, free heap %s, uptime %s\n",
		status.Mode, status.RSSI, humanSize(status.FreeHeap),
		(time.Duration(status.Uptime) * time.Second).String())

	if err := device.CheckCompat(status); err != nil {
		fmt.Fprintf(w, "  compat\tFAILED: %v\n", err)
		return
	}
	if warning := device.CompatWarning(status); warning != "" {
		fmt.Fprintf(w, "  compat\twarning: %s\n", warning)
	} else {
		fmt.Fprintf(w, "  compat\tok (tested against %s)\n", device.TestedVersion)
	}

	if !device.KnownModel(status.Device) {
		fmt.Fprintf(w, "  model\tunknown model %q; no optimization profile\n", status.Device)
	}

	// Confirm the sync root is readable, which is what a sync will need first.
	root := device.NewPath(d.Root)
	if _, err := c.List(ctx, root); err != nil {
		fmt.Fprintf(w, "  root %s\tnot readable: %v\n", root, err)
		return
	}
	fmt.Fprintf(w, "  root %s\tok\n", root)
}
