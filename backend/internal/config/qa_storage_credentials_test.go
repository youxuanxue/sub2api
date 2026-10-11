//go:build unit

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUS062_QAStoragePrivateCredentialsFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	valid := `{"access_key_id":"synthetic-access","secret_access_key":"synthetic-secret"}`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	storage := QACaptureStorageConfig{CredentialsFile: path}
	access, secret, err := storage.ReadCredentials()
	if err != nil || access != "synthetic-access" || secret != "synthetic-secret" {
		t.Fatal("private file did not resolve the explicit identity")
	}
	for _, body := range []string{`{`, `{}`, valid + `{}`, `{"access_key_id":"synthetic-access","secret_access_key":""}`, `{"access_key_id":"synthetic-access","secret_access_key":"synthetic-secret","extra":"bad"}`, strings.Repeat("x", 16385)} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		access, secret, err = storage.ReadCredentials()
		if err == nil || access != "" || secret != "" || strings.Contains(err.Error(), "synthetic-") {
			t.Fatal("invalid file must fail without leaking or returning credentials")
		}
	}
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := storage.ReadCredentials(); err == nil {
		t.Fatal("public file accepted")
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (QACaptureStorageConfig{CredentialsFile: link}).ReadCredentials(); err == nil {
		t.Fatal("symlink accepted")
	}
	storage.AccessKeyID = "inline"
	if _, _, err := storage.ReadCredentials(); err == nil {
		t.Fatal("ambiguous identity accepted")
	}
	if _, _, err := (QACaptureStorageConfig{CredentialsFile: path + ".missing"}).ReadCredentials(); err == nil {
		t.Fatal("missing file fell back")
	}
}
