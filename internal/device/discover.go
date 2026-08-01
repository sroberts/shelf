package device

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
)

// UDP discovery.
//
// The device answers a "hello" datagram on port 8134 with
//
//	crosspoint (on <hostname>);81
//
// where the trailing field is the WebSocket upload port.

const (
	discoveryPayload = "hello"
	// discoveryTimeout bounds a discovery sweep. The device is a
	// microcontroller and may take a moment to answer.
	discoveryTimeout = 2 * time.Second
)

// discoveryReply matches the documented response format.
var discoveryReply = regexp.MustCompile(`^crosspoint\s*\(on\s+([^)]+)\)\s*;\s*(\d+)`)

// Discovered is a device found on the network.
type Discovered struct {
	Hostname string
	Addr     string // IP address that answered
	WSPort   int
}

// Host returns the address to build a client from.
func (d Discovered) Host() string { return d.Addr }

// Discover broadcasts a discovery request on every suitable interface and
// collects replies until the context expires or timeout elapses.
//
// Every interface is enumerated rather than sending a single datagram to
// 255.255.255.255, because a global broadcast is silently dropped on macOS when
// the route is ambiguous, which is exactly the multi-interface case a laptop is
// usually in.
func Discover(ctx context.Context, timeout time.Duration) ([]Discovered, error) {
	if timeout <= 0 {
		timeout = discoveryTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addrs, err := broadcastAddrs()
	if err != nil {
		return nil, err
	}
	// Fall back to the global broadcast address when no interface reports one.
	if len(addrs) == 0 {
		addrs = []string{"255.255.255.255"}
	}

	var (
		mu    sync.Mutex
		found = map[string]Discovered{}
		wg    sync.WaitGroup
	)

	for _, addr := range addrs {
		wg.Add(1)
		go func(broadcast string) {
			defer wg.Done()
			for _, d := range probe(ctx, broadcast) {
				mu.Lock()
				// Deduplicate by address; several interfaces may reach the
				// same device.
				if _, seen := found[d.Addr]; !seen {
					found[d.Addr] = d
				}
				mu.Unlock()
			}
		}(addr)
	}
	wg.Wait()

	out := make([]Discovered, 0, len(found))
	for _, d := range found {
		out = append(out, d)
	}
	return out, nil
}

// probe sends one discovery datagram and gathers replies until the context ends.
func probe(ctx context.Context, broadcast string) []Discovered {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil
	}
	defer conn.Close()

	target, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", broadcast, DiscoveryPort))
	if err != nil {
		return nil
	}
	if _, err := conn.WriteTo([]byte(discoveryPayload), target); err != nil {
		return nil
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(discoveryTimeout)
	}
	_ = conn.SetReadDeadline(deadline)

	var out []Discovered
	buf := make([]byte, 512)

	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return out // deadline reached
		}
		if d, ok := parseReply(string(buf[:n]), from); ok {
			out = append(out, d)
		}
	}
}

// parseReply decodes a discovery response.
func parseReply(reply string, from net.Addr) (Discovered, bool) {
	m := discoveryReply.FindStringSubmatch(strings.TrimSpace(reply))
	if m == nil {
		return Discovered{}, false
	}

	host, _, err := net.SplitHostPort(from.String())
	if err != nil {
		host = from.String()
	}

	port := WebSocketPort
	if _, scanErr := fmt.Sscanf(m[2], "%d", &port); scanErr != nil {
		port = WebSocketPort
	}

	return Discovered{
		Hostname: strings.TrimSpace(m[1]),
		Addr:     host,
		WSPort:   port,
	}, true
}

// broadcastAddrs lists the broadcast address of every up, non-loopback IPv4
// interface.
func broadcastAddrs() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("enumerate network interfaces: %w", err)
	}

	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 ||
			iface.Flags&net.FlagLoopback != 0 ||
			iface.Flags&net.FlagBroadcast == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			if b := broadcastOf(ipnet); b != "" {
				out = append(out, b)
			}
		}
	}
	return out, nil
}

// broadcastOf computes the broadcast address of an IPv4 network.
func broadcastOf(n *net.IPNet) string {
	ip := n.IP.To4()
	mask := net.IP(n.Mask).To4()
	if ip == nil || mask == nil {
		return ""
	}

	out := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		out[i] = ip[i] | ^mask[i]
	}
	return out.String()
}

// Resolve turns a configured host into a reachable client, discovering the
// device when no host is set.
func Resolve(ctx context.Context, host string, opts ...Option) (*Client, error) {
	if host != "" {
		c := New(host, opts...)
		if err := c.Ping(ctx); err != nil {
			return nil, err
		}
		return c, nil
	}

	found, err := Discover(ctx, discoveryTimeout)
	if err != nil {
		return nil, err
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("%w: no device answered discovery on UDP %d",
			ErrNotInTransfer, DiscoveryPort)
	case 1:
		return New(found[0].Host(), opts...), nil
	default:
		var names []string
		for _, d := range found {
			names = append(names, fmt.Sprintf("%s (%s)", d.Hostname, d.Addr))
		}
		return nil, fmt.Errorf("several devices answered discovery: %s; set host in config.toml",
			strings.Join(names, ", "))
	}
}
