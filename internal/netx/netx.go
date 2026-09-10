package netx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
)

var localCIDRs []*net.IPNet

func init() {
	// Parsing hardcoded CIDR strings should never fail, if in case it does, let's
	// fail it at start.
	rawCIDRs := []string{
		// https://datatracker.ietf.org/doc/html/rfc5735:
		"127.0.0.0/8",        // Loopback
		"0.0.0.0/8",          // "This" network
		"100.64.0.0/10",      // Shared address space
		"169.254.0.0/16",     // Link local
		"172.16.0.0/12",      // Private-use networks
		"192.0.0.0/24",       // IETF Protocol assignments
		"192.0.2.0/24",       // TEST-NET-1
		"192.88.99.0/24",     // 6to4 Relay anycast
		"192.168.0.0/16",     // Private-use networks
		"198.18.0.0/15",      // Network interconnect
		"198.51.100.0/24",    // TEST-NET-2
		"203.0.113.0/24",     // TEST-NET-3
		"255.255.255.255/32", // Limited broadcast

		// https://datatracker.ietf.org/doc/html/rfc1918:
		"10.0.0.0/8", // Private-use networks

		// https://datatracker.ietf.org/doc/html/rfc6890:
		"::1/128",   // Loopback
		"FC00::/7",  // Unique local address
		"FE80::/10", // Multicast address
	}
	for _, raw := range rawCIDRs {
		_, cidr, err := net.ParseCIDR(raw)
		if err != nil {
			panic(fmt.Sprintf("parse CIDR %q: %v", raw, err))
		}
		localCIDRs = append(localCIDRs, cidr)
	}
}

// IsBlockedLocalHostname returns true if given hostname is resolved to a local
// network address that is implicitly blocked (i.e. not exempted from the
// allowlist).
func IsBlockedLocalHostname(hostname string, allowlist []string) bool {
	for _, allow := range allowlist {
		if hostname == allow || allow == "*" {
			return false
		}
	}

	ips, err := net.LookupIP(hostname)
	if err != nil {
		return true
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return true
		}
	}
	return false
}

func isBlockedIP(ip net.IP) bool {
	for _, cidr := range localCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// allowlistPermitsIP reports whether an allowlist entry explicitly permits ip:
// "*", an exact IP literal, or a CIDR block that contains it. Host name entries
// are handled by the caller before resolution.
func allowlistPermitsIP(ip net.IP, allowlist []string) bool {
	for _, entry := range allowlist {
		if entry == "*" {
			return true
		}
		if entryIP := net.ParseIP(entry); entryIP != nil {
			if entryIP.Equal(ip) {
				return true
			}
			continue
		}
		if _, cidr, err := net.ParseCIDR(entry); err == nil && cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// SafeDialContext returns a net.Dialer.DialContext function that refuses to
// connect to implicitly blocked local network addresses. It resolves the host
// itself and dials a vetted IP directly, so a name that resolves to a public
// address at pre-flight check time and to a private one at connect time (DNS
// rebinding) cannot get through. An allowlist entry that matches the requested
// host by name, or "*", skips the check; entries may also be an IP or CIDR.
func SafeDialContext(allowlist []string, connectTimeout, readWriteTimeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: connectTimeout}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}

		dial := func(address string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			if readWriteTimeout > 0 {
				if err := conn.SetDeadline(time.Now().Add(readWriteTimeout)); err != nil {
					_ = conn.Close()
					return nil, err
				}
			}
			return conn, nil
		}

		for _, entry := range allowlist {
			if entry == "*" || strings.EqualFold(entry, host) {
				return dial(addr)
			}
		}

		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}

		var lastErr error
		for _, ipAddr := range ips {
			ip := ipAddr.IP
			if v4 := ip.To4(); v4 != nil {
				ip = v4
			}
			if isBlockedIP(ip) && !allowlistPermitsIP(ip, allowlist) {
				lastErr = errors.Newf("dial %s: resolved address %s is in a blocked local network", host, ip)
				continue
			}
			conn, err := dial(net.JoinHostPort(ip.String(), port))
			if err != nil {
				lastErr = err
				continue
			}
			return conn, nil
		}
		if lastErr == nil {
			lastErr = errors.Newf("dial %s: no address resolved", host)
		}
		return nil, lastErr
	}
}

// SafeHTTPTransport returns an *http.Transport whose DialContext is guarded by
// [SafeDialContext]. TLS configuration is left nil so callers (or
// internal/httplib) populate it; the environment proxy is honored to preserve
// existing outbound behavior.
func SafeHTTPTransport(allowlist []string, connectTimeout, readWriteTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:       http.ProxyFromEnvironment,
		DialContext: SafeDialContext(allowlist, connectTimeout, readWriteTimeout),
	}
}
