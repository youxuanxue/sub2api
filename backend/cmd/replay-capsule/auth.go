package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// authCheck runs inside the isolated gateway network namespace. Credentials
// arrive over stdin, never argv, environment, files, or diagnostic output.
func authCheck(input io.Reader, output io.Writer) error {
	var in struct {
		Key       string `json:"key"`
		Body      []byte `json:"body"`
		Port      int    `json:"port"`
		RequestID string `json:"request_id"`
	}
	raw, err := io.ReadAll(io.LimitReader(input, 16385))
	if err != nil || len(raw) > 16384 {
		return errors.New("invalid auth check input")
	}
	defer clear(raw)
	if json.Unmarshal(raw, &in) != nil || in.Key == "" || in.Port < 1 || in.Port > 65535 {
		return errors.New("invalid auth check input")
	}
	body := in.Body
	if len(body) == 0 || len(body) > 4096 || !json.Valid(body) {
		return errors.New("invalid auth check body")
	}
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(in.Port)+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return errors.New("auth request failed")
	}
	req.Header.Set("Authorization", "Bearer "+in.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Request-ID", in.RequestID)
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	started := time.Now()
	res, err := client.Do(req)
	if err != nil {
		return errors.New("auth request failed")
	}
	defer func() { _ = res.Body.Close() }()
	response, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(response) > 65536 {
		return errors.New("auth response incomplete")
	}
	defer clear(response)
	var value struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(response, &value) != nil {
		return errors.New("invalid auth response")
	}
	code := value.Code
	if code == "" {
		code = value.Error.Code
	}
	switch code {
	case "INVALID_API_KEY", "API_KEY_DISABLED", "API_KEY_EXPIRED", "insufficient_quota":
	default:
		code = "unexpected"
	}
	id := res.Header.Get("X-Request-ID")
	if !regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`).MatchString(id) {
		id = ""
	}
	sum := sha256.Sum256(response)
	return json.NewEncoder(output).Encode(map[string]any{"http_status": res.StatusCode, "error_code": code, "response_request_id": id,
		"response_sha256": hex.EncodeToString(sum[:]), "response_bytes": len(response), "elapsed_ms": time.Since(started).Milliseconds()})
}
