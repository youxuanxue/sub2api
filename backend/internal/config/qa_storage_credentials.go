package config

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

// ReadCredentials keeps QA identities independent of the process-wide AWS
// chain (which may still serve media or telemetry). An explicit file fails
// closed; it never falls back to the host identity. Errors contain no secrets.
func (s QACaptureStorageConfig) ReadCredentials() (string, string, error) {
	path := strings.TrimSpace(s.CredentialsFile)
	if path == "" {
		return s.AccessKeyID, s.SecretAccessKey, nil
	}
	invalid := errors.New("QA storage credentials require a private regular JSON file with access_key_id and secret_access_key")
	if s.AccessKeyID != "" || s.SecretAccessKey != "" {
		return "", "", errors.New("QA storage file and inline credentials cannot be combined")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return "", "", invalid
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", invalid
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !os.SameFile(before, info) || !info.Mode().IsRegular() ||
		(info.Mode().Perm() != 0400 && info.Mode().Perm() != 0600) || info.Size() > 16384 {
		return "", "", invalid
	}
	var value struct {
		AccessKey string `json:"access_key_id"`
		SecretKey string `json:"secret_access_key"`
	}
	decoder := json.NewDecoder(io.LimitReader(f, 16385))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || strings.TrimSpace(value.AccessKey) == "" || strings.TrimSpace(value.SecretKey) == "" {
		return "", "", invalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return "", "", invalid
	}
	return value.AccessKey, value.SecretKey, nil
}
