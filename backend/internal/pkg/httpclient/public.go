package httpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// NewPublicClient resolves once, validates every answer, then dials a validated
// address directly. An ordinary second DNS lookup would allow rebinding.
// Redirects use the same transport. Environment proxies are excluded because
// they would resolve targets outside this boundary.
func NewPublicClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = publicDialContext(net.DefaultResolver.LookupIPAddr, dialer.DialContext)
	return &http.Client{Transport: transport, Timeout: timeout}
}

func publicDialContext(
	lookup func(context.Context, string) ([]net.IPAddr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := lookup(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve download host: %w", err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("download host has no addresses")
		}
		for _, ip := range ips {
			if !ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.Zone != "" || sharedAddressSpace.Contains(ip.IP) {
				return nil, fmt.Errorf("download host resolves to a non-public address")
			}
		}
		for _, ip := range ips {
			conn, dialErr := dial(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			err = dialErr
		}
		return nil, err
	}
}

var sharedAddressSpace = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
