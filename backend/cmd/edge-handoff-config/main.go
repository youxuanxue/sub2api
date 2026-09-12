// edge-handoff-config prepares a NEW local trust bundle from a public manifest.
// It never contacts a deployment, overwrites a file, or prints key material.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"io"
	"os"
	"path/filepath"
)

type edgeSpec struct {
	ID          string `json:"id"`
	Origin      string `json:"origin"`
	AdminUserID int64  `json:"admin_user_id"`
	KeyID       string `json:"key_id"`
}
type manifest struct {
	Issuer string     `json:"issuer"`
	Edges  []edgeSpec `json:"edges"`
}

func prepare(m manifest, out string) error {
	if !config.EdgeHandoffOrigin(m.Issuer) || len(m.Edges) == 0 {
		return errors.New("invalid issuer or empty edges")
	}
	seen := map[string]bool{}
	origins := map[string]bool{}
	for _, edge := range m.Edges {
		if !config.HandoffKeyID(edge.ID) || !config.HandoffKeyID(edge.KeyID) || !config.EdgeHandoffOrigin(edge.Origin) || edge.AdminUserID <= 0 || seen[edge.ID] || origins[edge.Origin] {
			return errors.New("invalid or duplicate edge")
		}
		seen[edge.ID] = true
		origins[edge.Origin] = true
	}
	prod := config.EdgeHandoffConfig{Version: 1, Issuer: m.Issuer, Signers: map[string]config.EdgeHandoffSigner{}}
	files := map[string]config.EdgeHandoffConfig{}
	for _, edge := range m.Edges {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		prod.Signers[edge.ID] = config.EdgeHandoffSigner{Origin: edge.Origin, KeyID: edge.KeyID, Seed: base64.RawURLEncoding.EncodeToString(key.Seed())}
		files["edge-"+edge.ID+".json"] = config.EdgeHandoffConfig{Version: 1, Receiver: &config.EdgeHandoffReceiver{Issuer: m.Issuer, Origin: edge.Origin, AdminUserID: edge.AdminUserID, PublicKeys: map[string]string{edge.KeyID: base64.RawURLEncoding.EncodeToString(pub)}}}
	}
	files["prod.json"] = prod
	if err := os.Mkdir(out, 0700); err != nil {
		return errors.New("output directory must not exist")
	}
	for name, cfg := range files {
		raw, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(out, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("cannot create protected output")
		}
		_, writeErr := f.Write(append(raw, '\n'))
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			return errors.New("cannot write protected output")
		}
	}
	return nil
}
func main() {
	input := flag.String("manifest", "", "public JSON manifest path")
	out := flag.String("out", "", "new protected local directory")
	flag.Parse()
	if *input == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	f, err := os.Open(*input)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot read manifest")
		os.Exit(1)
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(io.LimitReader(f, 65537))
	dec.DisallowUnknownFields()
	var m manifest
	if dec.Decode(&m) != nil {
		fmt.Fprintln(os.Stderr, "invalid manifest")
		os.Exit(1)
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		fmt.Fprintln(os.Stderr, "invalid manifest suffix")
		os.Exit(1)
	}
	if err := prepare(m, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Protected local bundle prepared. Nothing deployed.")
}
