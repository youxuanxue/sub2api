package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
)

const DefaultEdgeHandoffFile = "/app/data/edge-handoff.json"

type EdgeHandoffSigner struct {
	Origin string `json:"origin"`
	KeyID  string `json:"key_id"`
	Seed   string `json:"seed"`
}
type EdgeHandoffReceiver struct {
	Origin      string            `json:"origin"`
	Issuer      string            `json:"issuer"`
	AdminUserID int64             `json:"admin_user_id"`
	PublicKeys  map[string]string `json:"public_keys"`
}
type EdgeHandoffConfig struct {
	Version  int                          `json:"version"`
	Issuer   string                       `json:"issuer"`
	Signers  map[string]EdgeHandoffSigner `json:"signers"`
	Receiver *EdgeHandoffReceiver         `json:"receiver"`
}

// EdgeHandoffOrigin accepts exact origins only. HTTP is restricted to loopback
// so the same production protocol can be tested on an isolated workstation.
func EdgeHandoffOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" || u.String() != raw {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && ip != nil && ip.IsLoopback()
}
func HandoffKeyID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}
func LoadEdgeHandoffConfig(path string) (*EdgeHandoffConfig, error) {
	disabled := &EdgeHandoffConfig{}
	if path == "" {
		return disabled, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return disabled, nil
	}
	if err != nil {
		return nil, errors.New("cannot read edge handoff configuration")
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(io.LimitReader(f, 64*1024+1))
	dec.DisallowUnknownFields()
	var cfg EdgeHandoffConfig
	if dec.Decode(&cfg) != nil {
		return nil, errors.New("invalid edge handoff configuration")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid edge handoff configuration suffix")
	}
	if cfg.Version != 1 {
		return nil, errors.New("unsupported edge handoff configuration version")
	}
	if len(cfg.Signers) > 0 {
		info, err := f.Stat()
		if err != nil || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("edge handoff signing configuration requires private file permissions")
		}
		if !EdgeHandoffOrigin(cfg.Issuer) {
			return nil, errors.New("invalid edge handoff issuer")
		}
		for id, signer := range cfg.Signers {
			seed, err := base64.RawURLEncoding.Strict().DecodeString(signer.Seed)
			if !HandoffKeyID(id) || !HandoffKeyID(signer.KeyID) || !EdgeHandoffOrigin(signer.Origin) || err != nil || len(seed) != ed25519.SeedSize {
				return nil, fmt.Errorf("invalid edge handoff signer for %q", id)
			}
		}
	}
	if r := cfg.Receiver; r != nil {
		if !EdgeHandoffOrigin(r.Origin) || !EdgeHandoffOrigin(r.Issuer) || r.AdminUserID <= 0 || len(r.PublicKeys) == 0 {
			return nil, errors.New("invalid edge handoff receiver")
		}
		for id, key := range r.PublicKeys {
			pub, err := base64.RawURLEncoding.Strict().DecodeString(key)
			if !HandoffKeyID(id) || err != nil || len(pub) != ed25519.PublicKeySize {
				return nil, errors.New("invalid edge handoff verification key")
			}
		}
	}
	return &cfg, nil
}
