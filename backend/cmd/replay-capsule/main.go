// replay-capsule decrypts release evidence offline; it never starts a gateway.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/replaycapture"
)

func run(args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("expected keygen or decrypt")
	}
	if len(args) == 1 && args[0] == "auth-check" {
		return authCheck(os.Stdin, output)
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "", "private capsule/key directory")
	keyPath := flags.String("key", "", "PKCS8 private key file")
	if err := flags.Parse(args[1:]); err != nil {
		return errors.New("invalid capsule arguments")
	}
	if *dir == "" || flags.NArg() != 0 {
		return errors.New("capsule directory required")
	}
	switch args[0] {
	case "keygen":
		// Refuse existing destinations instead of rotating a key behind retained data.
		if err := os.Mkdir(*dir, 0700); err != nil {
			return err
		}
		key, err := rsa.GenerateKey(rand.Reader, 3072)
		if err != nil {
			return err
		}
		private, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return err
		}
		public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(*dir, "private.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(*dir, "public.pem"), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public}), 0644); err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(map[string]string{"key_id": replaycapture.KeyID(&key.PublicKey)})
	case "decrypt":
		info, err := os.Lstat(*keyPath)
		if err != nil {
			return errors.New("private key unavailable")
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
			return errors.New("private key must be a bounded regular 0600 file")
		}
		raw, err := os.ReadFile(*keyPath)
		if err != nil {
			return errors.New("private key unavailable")
		}
		key, err := replaycapture.PrivateKey(raw)
		clear(raw)
		if err != nil {
			return err
		}
		envelopes, err := replaycapture.ReadEnvelopes(*dir)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(output)
		for _, envelope := range envelopes {
			req, err := replaycapture.Decrypt(key, envelope, time.Now())
			if errors.Is(err, replaycapture.ErrExpired) {
				continue
			}
			if err != nil {
				return err
			}
			sum := sha256.Sum256(envelope)
			if err = encoder.Encode(map[string]any{"envelope_sha256": hex.EncodeToString(sum[:]), "request": req}); err != nil {
				return err
			}
			clear(req.Body)
		}
		return nil
	default:
		return errors.New("expected keygen or decrypt")
	}
}
func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "replay capsule command failed (no payload logged)")
		os.Exit(1)
	}
}
