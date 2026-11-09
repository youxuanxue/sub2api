package tlsfingerprint

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
