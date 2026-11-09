package tlsfingerprint

// HTTP/2 flow-control window policy carried on a per-request Profile copy.
// The service layer resolves it from account settings; the repository layer
// reads it when building the HTTP/2 transport. It never changes the ClientHello.
const (
	// HTTP2WindowsProfile (default, also "") keeps the windows the mimicked
	// client advertises (e.g. Codex CLI 2 MiB stream / ~5 MiB connection).
	HTTP2WindowsProfile = "profile"
	// HTTP2WindowsGoDefault leaves MaxReceiveBufferPerStream/PerConnection at
	// Go's defaults (4 MiB / 1 GiB) for multiplexed multi-user connections.
	HTTP2WindowsGoDefault = "go_default"
)

// UsesGoDefaultHTTP2Windows reports whether the profile opts out of the
// mimicked client's HTTP/2 flow-control windows.
func (p *Profile) UsesGoDefaultHTTP2Windows() bool {
	return p != nil && p.HTTP2Windows == HTTP2WindowsGoDefault
}

// WithHTTP1OnlyALPN derives a copy that advertises only http/1.1 while keeping
// the ALPN extension (and every other extension) in place, so an HTTP/1.1-only
// transport never negotiates h2 it cannot speak. Mirrors the WS derivation in
// service/openai_ws_client_tk_tls.go, minus dropping the extension.
func (p *Profile) WithHTTP1OnlyALPN() *Profile {
	if p == nil {
		return nil
	}
	out := *p
	out.ALPNProtocols = []string{"http/1.1"}
	return &out
}
