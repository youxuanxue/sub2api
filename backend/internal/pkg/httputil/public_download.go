package httputil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var publicDownloadBlockedHostnames = map[string]struct{}{
	"localhost":                  {},
	"localhost.localdomain":      {},
	"metadata":                   {},
	"metadata.google.internal":   {},
	"metadata.goog":              {},
	"instance-data":              {},
	"instance-data.ec2.internal": {},
}

var publicDownloadBlockedCIDRs = mustParsePublicDownloadCIDRs([]string{
	"127.0.0.0/8",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16",
	"100.64.0.0/10",
	"0.0.0.0/8",
	"::1/128",
	"fc00::/7",
	"fe80::/10",
	"::/128",
})

type PublicURLDownload struct {
	Body        []byte
	ContentType string
}

// DownloadPublicURL fetches a public HTTP(S) object with SSRF and size guards.
// It is for server-side re-hosting of URLs returned by trusted upstream APIs:
// private/link-local targets are rejected both before the request and at dial
// time, redirects are re-validated, and the response body is hard-capped.
func DownloadPublicURL(ctx context.Context, raw string, maxBytes int64, timeout time.Duration) (*PublicURLDownload, error) {
	if maxBytes <= 0 {
		return nil, errors.New("maxBytes must be positive")
	}
	parsed, err := validatePublicDownloadURL(raw)
	if err != nil {
		return nil, err
	}

	reqCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "video/*,application/octet-stream;q=0.9,*/*;q=0.1")

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           publicDownloadDialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			_, err := validatePublicDownloadURL(req.URL.String())
			return err
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	return readPublicDownloadResponse(resp, maxBytes)
}

func readPublicDownloadResponse(resp *http.Response, maxBytes int64) (*PublicURLDownload, error) {
	if resp == nil {
		return nil, errors.New("empty response")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("download too large: content-length %d > %d", resp.ContentLength, maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("download too large: body exceeds %d bytes", maxBytes)
	}
	if len(body) == 0 {
		return nil, errors.New("download body is empty")
	}
	return &PublicURLDownload{Body: body, ContentType: resp.Header.Get("Content-Type")}, nil
}

func validatePublicDownloadURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid url: %s", trimmed)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return nil, fmt.Errorf("invalid url scheme: %s", parsed.Scheme)
	}
	if parsed.User != nil {
		return nil, errors.New("url userinfo is not allowed")
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" || isPublicDownloadBlockedHostname(host) {
		return nil, fmt.Errorf("host is not allowed: %s", host)
	}
	if ip := net.ParseIP(host); ip != nil && isPublicDownloadPrivateIP(ip) {
		return nil, fmt.Errorf("host is not allowed: %s", host)
	}
	if port := parsed.Port(); port != "" {
		num, err := strconv.Atoi(port)
		if err != nil || num <= 0 || num > 65535 {
			return nil, fmt.Errorf("invalid port: %s", port)
		}
	}
	return parsed, nil
}

func publicDownloadDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPublicDownloadPrivateIP(ip) {
			return nil, &net.AddrError{Err: "blocked by SSRF policy", Addr: address}
		}
		return (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
	}
	if isPublicDownloadBlockedHostname(host) {
		return nil, &net.AddrError{Err: "blocked by SSRF policy", Addr: address}
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, &net.AddrError{Err: "no addresses for host", Addr: host}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, addr := range addrs {
		if isPublicDownloadPrivateIP(addr.IP) {
			lastErr = &net.AddrError{Err: "blocked by SSRF policy", Addr: addr.IP.String()}
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = &net.AddrError{Err: "no usable addresses", Addr: host}
	}
	return nil, lastErr
}

func isPublicDownloadBlockedHostname(hostname string) bool {
	if hostname == "" {
		return true
	}
	host := strings.ToLower(hostname)
	if strings.HasSuffix(host, ".localhost") {
		return true
	}
	_, blocked := publicDownloadBlockedHostnames[host]
	return blocked
}

func isPublicDownloadPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, n := range publicDownloadBlockedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func mustParsePublicDownloadCIDRs(cidrs []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("public_download: invalid CIDR " + c + ": " + err.Error())
		}
		out = append(out, n)
	}
	return out
}
