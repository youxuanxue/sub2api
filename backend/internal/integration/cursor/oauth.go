package cursor

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const OAuthAPIBaseURL = "https://api2.cursor.sh"

type OAuthLogin struct {
	ID       string
	Verifier string
	URL      string
}
type OAuthCredentials struct {
	AccessToken  string    `json:"-"`
	RefreshToken string    `json:"-"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// NewOAuthLogin mirrors the official CLI PKCE flow. The caller owns persistence,
// authorization-session expiry and administrator binding in its shared store.
func NewOAuthLogin() (OAuthLogin, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return OAuthLogin{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(random[:])
	challenge := sha256.Sum256([]byte(verifier))
	id := uuid.NewString()
	params := url.Values{"challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "uuid": {id}, "mode": {"login"}, "redirectTarget": {"cli"}}
	return OAuthLogin{ID: id, Verifier: verifier, URL: "https://cursor.com/loginDeepControl?" + params.Encode()}, nil
}

// PollOAuth performs one poll. A pending login returns nil, nil. It neither
// persists credentials nor treats the returned refresh JWT as a user API key.
func PollOAuth(ctx context.Context, login OAuthLogin, do func(*http.Request) (*http.Response, error)) (*OAuthCredentials, error) {
	if _, err := uuid.Parse(login.ID); err != nil || len(login.Verifier) != 43 {
		return nil, errors.New("invalid Cursor authorization session")
	}
	endpoint := OAuthAPIBaseURL + "/auth/poll?" + url.Values{"uuid": {login.ID}, "verifier": {login.Verifier}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	setOAuthClientHeaders(req)
	resp, err := do(req)
	if err != nil {
		return nil, errors.New("cursor authorization poll failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 404 {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		return nil, &Error{Status: resp.StatusCode, Message: fmt.Sprintf("cursor authorization returned HTTP %d", resp.StatusCode)}
	}
	var wire struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeOAuthResponse(resp.Body, &wire); err != nil {
		return nil, err
	}
	expires, err := OAuthTokenExpiry(wire.AccessToken)
	if err != nil || !expires.After(time.Now()) {
		return nil, errors.New("cursor authorization returned an expired or invalid access token")
	}
	return &OAuthCredentials{AccessToken: wire.AccessToken, RefreshToken: wire.RefreshToken, ExpiresAt: expires}, nil
}

// OAuthTokenExpiry reads lifecycle metadata only; it does not authenticate the
// token. The authenticated upstream catalog remains the credential validation.
func OAuthTokenExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 16384 {
		return time.Time{}, errors.New("invalid Cursor token format")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, errors.New("invalid Cursor token payload")
	}
	var payload struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Exp <= 0 {
		return time.Time{}, errors.New("cursor token has no valid expiry")
	}
	return time.Unix(payload.Exp, 0).UTC(), nil
}

func setOAuthClientHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Cursor-Client-Version", AgentClientVersion)
	req.Header.Set("X-Cursor-Client-Type", "cli")
	// The UUID is independent of the PKCE verifier and contains no credentials.
	req.Header.Set("X-Request-Id", uuid.NewString())
}
func decodeOAuthResponse(reader io.Reader, output any) error {
	raw, err := io.ReadAll(io.LimitReader(reader, (2<<20)+1))
	if err != nil || len(raw) > 2<<20 {
		return errors.New("invalid Cursor authorization response size")
	}
	if json.Unmarshal(raw, output) != nil {
		return errors.New("invalid Cursor authorization response")
	}
	return nil
}

// OAuthModels preserves the authenticated catalog's exact base model ids and
// variant parameters. It does not promote the catalog to serving eligibility.
func OAuthModels(ctx context.Context, accessToken string, do func(*http.Request) (*http.Response, error)) ([]Model, error) {
	if accessToken == "" || strings.ContainsAny(accessToken, "\r\n") {
		return nil, errors.New("invalid Cursor access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthAPIBaseURL+"/aiserver.v1.AiService/AvailableModels", bytes.NewBufferString(`{"useModelParameters":true}`))
	if err != nil {
		return nil, err
	}
	setOAuthClientHeaders(req)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := do(req)
	if err != nil {
		return nil, errors.New("cursor model catalog request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, &Error{Status: resp.StatusCode, Message: fmt.Sprintf("cursor model catalog returned HTTP %d", resp.StatusCode)}
	}
	var wire struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"clientDisplayName"`
			Variants    []struct {
				Parameters []Parameter `json:"parameterValues"`
				Default    bool        `json:"isDefaultNonMaxConfig"`
				LegacySlug string      `json:"legacySlug"`
			} `json:"variants"`
		} `json:"models"`
	}
	if err := decodeOAuthResponse(resp.Body, &wire); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(wire.Models))
	seen := make(map[string]bool)
	for _, item := range wire.Models {
		if item.Name == "" || item.Name == "default" || item.Name == "auto" {
			continue
		}
		if seen[item.Name] {
			return nil, errors.New("cursor catalog returned duplicate models")
		}
		seen[item.Name] = true
		model := Model{ID: item.Name, DisplayName: item.DisplayName}
		for _, variant := range item.Variants {
			model.Variants = append(model.Variants, Variant{Params: variant.Parameters, IsDefault: variant.Default, LegacySlug: variant.LegacySlug})
		}
		models = append(models, model)
	}
	if len(models) == 0 {
		return nil, errors.New("cursor catalog has no fixed models")
	}
	return models, nil
}

// AgentVariantWireModel resolves only an exact catalog variant. ModelDetails
// requires its legacy slug even when RequestedModel carries a base id + params.
func AgentVariantWireModel(model Model, parameters []Parameter) (string, error) {
	for _, variant := range model.Variants {
		if sameParameters(variant.Params, parameters) && variant.LegacySlug != "" {
			return variant.LegacySlug, nil
		}
	}
	return "", errors.New("cursor catalog has no exact wire model for the selected parameters")
}
