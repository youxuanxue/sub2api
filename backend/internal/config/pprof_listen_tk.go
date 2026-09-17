package config

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// NormalizeServerPprofListen validates server.pprof_listen.
//
// Empty / "off" / "disabled" / "false" / "-" disables the listener.
// Non-empty addresses must resolve to a loopback host so the surface cannot be
// published through Caddy or a container port map by accident.
func NormalizeServerPprofListen(raw string) (addr string, enabled bool, err error) {
	s := strings.TrimSpace(raw)
	switch strings.ToLower(s) {
	case "", "off", "disabled", "false", "-":
		return "", false, nil
	}

	host, port, splitErr := net.SplitHostPort(s)
	if splitErr != nil {
		return "", false, fmt.Errorf("server.pprof_listen must be host:port: %w", splitErr)
	}
	if strings.TrimSpace(port) == "" {
		return "", false, errors.New("server.pprof_listen port is required")
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return "", false, errors.New("server.pprof_listen host is required (refusing wildcard bind)")
	}

	ips, lookupErr := net.LookupIP(host)
	if lookupErr != nil {
		ip := net.ParseIP(host)
		if ip == nil {
			return "", false, fmt.Errorf("server.pprof_listen host %q: %w", host, lookupErr)
		}
		ips = []net.IP{ip}
	}
	if len(ips) == 0 {
		return "", false, fmt.Errorf("server.pprof_listen host %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return "", false, fmt.Errorf("server.pprof_listen must bind loopback only; got non-loopback %s", ip.String())
		}
	}
	return net.JoinHostPort(host, port), true, nil
}
