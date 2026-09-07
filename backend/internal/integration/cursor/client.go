// Package cursor implements the authenticated internal Cursor SDK bridge contract.
package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const SecretHeader = "X-TokenKey-Bridge-Secret"
const TenantHeader = "X-Cursor-Agent-Tenant"
const DefaultBaseURL = "http://cursor-bridge:3927"

type Parameter struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}
type Variant struct {
	Params    []Parameter `json:"params"`
	IsDefault bool        `json:"isDefault,omitempty"`
}
type Model struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"displayName"`
	Variants    []Variant `json:"variants"`
}
type Authorization struct {
	ID           string    `json:"id"`
	State        string    `json:"state"`
	URL          string    `json:"authorization_url"`
	ExpiresAt    time.Time `json:"expires_at"`
	KeyExpiresAt time.Time `json:"key_expires_at"`
	Email        string    `json:"email,omitempty"`
	Models       []Model   `json:"models,omitempty"`
	Error        string    `json:"error,omitempty"`
}
type CredentialClaim struct {
	Authorization
	APIKey string `json:"api_key"`
	Claim  string `json:"claim"`
}
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

type Client struct {
	baseURL string
	secret  string
	http    *http.Client
}

func FromEnv() (*Client, error) {
	return NewClient(os.Getenv("CURSOR_BRIDGE_URL"), os.Getenv("CURSOR_BRIDGE_SECRET"))
}

func NewClient(baseURL, secret string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("cursor bridge URL is not configured correctly")
	}
	if len(secret) < 32 {
		return nil, errors.New("cursor bridge secret must contain at least 32 bytes")
	}
	return &Client{baseURL: strings.TrimRight(u.String(), "/"), secret: secret, http: &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) BaseURL() string { return c.baseURL }
func (c *Client) SetHeaders(header http.Header, owner string) {
	header.Set(SecretHeader, c.secret)
	header.Set(TenantHeader, owner)
}

func (c *Client) request(ctx context.Context, method, path, owner string, input, output any) error {
	if owner == "" {
		return errors.New("cursor bridge owner is required")
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	c.SetHeaders(req.Header, owner)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cursor bridge request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return errors.New("cursor bridge response could not be read")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Never propagate an untrusted response body that could contain a key.
		return &Error{Status: resp.StatusCode, Message: fmt.Sprintf("Cursor bridge returned HTTP %d", resp.StatusCode)}
	}
	if output == nil {
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return errors.New("invalid Cursor bridge response")
	}
	return nil
}

func authPath(id string) (string, error) {
	if len(id) != 36 {
		return "", errors.New("invalid authorization session")
	}
	for _, ch := range id {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && ch != '-' {
			return "", errors.New("invalid authorization session")
		}
	}
	return "/internal/auth/" + id, nil
}
func (c *Client) Start(ctx context.Context, owner string) (Authorization, error) {
	var result Authorization
	err := c.request(ctx, http.MethodPost, "/internal/auth", owner, map[string]string{"name": "TokenKey Cursor"}, &result)
	return result, err
}
func (c *Client) Status(ctx context.Context, owner, id string) (Authorization, error) {
	var result Authorization
	path, err := authPath(id)
	if err != nil {
		return result, err
	}
	err = c.request(ctx, http.MethodGet, path, owner, nil, &result)
	return result, err
}
func (c *Client) Cancel(ctx context.Context, owner, id string) error {
	path, err := authPath(id)
	if err != nil {
		return err
	}
	return c.request(ctx, http.MethodDelete, path, owner, nil, nil)
}
func (c *Client) Claim(ctx context.Context, owner, id string) (CredentialClaim, error) {
	var result CredentialClaim
	path, err := authPath(id)
	if err != nil {
		return result, err
	}
	err = c.request(ctx, http.MethodPost, path+"/claim", owner, nil, &result)
	return result, err
}
func (c *Client) Settle(ctx context.Context, owner, id, claim string, success bool) error {
	path, err := authPath(id)
	if err != nil {
		return err
	}
	return c.request(ctx, http.MethodPost, path+"/settle", owner, map[string]any{"claim": claim, "success": success}, nil)
}

// DefaultParameters retains the catalog's default variant with regular speed
// where the catalog explicitly exposes the equivalent non-Fast variant.
func DefaultParameters(model Model) []Parameter {
	var selected []Parameter
	for _, variant := range model.Variants {
		if variant.IsDefault {
			selected = append([]Parameter(nil), variant.Params...)
			break
		}
	}
	for i := range selected {
		if selected[i].ID == "fast" {
			selected[i].Value = "false"
		}
	}
	for _, variant := range model.Variants {
		if sameParameters(selected, variant.Params) {
			return selected
		}
	}
	for _, variant := range model.Variants {
		if variant.IsDefault {
			return append([]Parameter(nil), variant.Params...)
		}
	}
	return nil
}
func sameParameters(a, b []Parameter) bool {
	if len(a) != len(b) {
		return false
	}
	for _, p := range a {
		found := false
		for _, q := range b {
			if p == q {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
