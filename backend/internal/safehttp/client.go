package safehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"time"
)

func NewDownloadClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		// Provider result downloads must resolve and dial the validated public
		// address ourselves. An environment HTTP proxy would bypass this DNS/IP
		// check and re-introduce an SSRF path through CONNECT.
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, address := range addresses {
				if !isPublicIP(address) {
					continue
				}
				connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
				if dialErr == nil {
					return connection, nil
				}
				err = dialErr
			}
			if err != nil {
				return nil, err
			}
			return nil, errors.New("download host has no public IP address")
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func isPublicIP(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range nonPublicSpecialPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

// Go's IsGlobalUnicast reports whether an address has unicast syntax, not
// whether IANA designates it globally reachable. Keep result downloads away
// from special-purpose ranges that can expose local infrastructure or bypass
// DNS-based SSRF checks.
var nonPublicSpecialPrefixes = [...]netip.Prefix{
	// IANA IPv4 Special-Purpose Address Space. Aggregate protocol-assignment
	// ranges are rejected conservatively, including their anycast exceptions.
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),

	// IANA IPv6 Special-Purpose Address Space. Aggregate protocol-assignment
	// ranges and the well-known NAT64 prefix are rejected conservatively: a
	// translated address can otherwise encode a forbidden IPv4 destination
	// after this process has validated the AAAA record.
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
}
