// Package cursor implements Cursor CLI authorization and stateless model calls.
package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const DefaultBaseURL = AgentBaseURL
const authorizationTTL = 10 * time.Minute

type Parameter struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}
type Variant struct {
	Params     []Parameter `json:"params"`
	IsDefault  bool        `json:"isDefault,omitempty"`
	LegacySlug string      `json:"legacySlug,omitempty"`
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
	APIKey string `json:"-"`
	Claim  string `json:"-"`
}
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// Only short-lived authorization state is shared. No inference state is stored.
type authorizationSession struct {
	Authorization
	Owner       string
	Login       OAuthLogin
	AccessToken string
	Claim       string
}
type Client struct {
	rdb *redis.Client
	do  func(*http.Request) (*http.Response, error)
}

func NewClient(rdb *redis.Client, do func(*http.Request) (*http.Response, error)) *Client {
	return &Client{rdb: rdb, do: do}
}
func (c *Client) Enabled() bool   { return c != nil && c.rdb != nil && c.do != nil }
func (c *Client) BaseURL() string { return AgentBaseURL }
func authKey(id string) (string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return "", errors.New("invalid Cursor authorization session")
	}
	return "oauth:cursor:" + id, nil
}
func (c *Client) load(ctx context.Context, owner, id string) (authorizationSession, string, error) {
	var session authorizationSession
	if !c.Enabled() {
		return session, "", errors.New("cursor authorization store unavailable")
	}
	key, err := authKey(id)
	if err != nil {
		return session, "", err
	}
	raw, err := c.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return session, "", &Error{404, "Cursor authorization expired or not found"}
	}
	if err != nil {
		return session, "", errors.New("cursor authorization store unavailable")
	}
	if json.Unmarshal([]byte(raw), &session) != nil {
		return session, "", errors.New("invalid Cursor authorization state")
	}
	if owner == "" || session.Owner != owner {
		return authorizationSession{}, "", &Error{404, "Cursor authorization not found"}
	}
	if !session.ExpiresAt.After(time.Now()) {
		return session, "", &Error{410, "Cursor authorization expired"}
	}
	return session, raw, nil
}

// Compare-and-set preserves TTL and prevents poll/cancel/import races from
// resurrecting a session or claiming a credential twice.
var replaceAuthorization = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
if ARGV[2] == '' then redis.call('DEL', KEYS[1])
else redis.call('SET', KEYS[1], ARGV[2], 'KEEPTTL') end
return 1`)

func (c *Client) replace(ctx context.Context, id, old string, next *authorizationSession) error {
	key, err := authKey(id)
	if err != nil {
		return err
	}
	var raw []byte
	if next != nil {
		raw, err = json.Marshal(next)
		if err != nil {
			return err
		}
	}
	ok, err := replaceAuthorization.Run(ctx, c.rdb, []string{key}, old, string(raw)).Int()
	if err != nil {
		return errors.New("cursor authorization store unavailable")
	}
	if ok != 1 {
		return &Error{409, "Cursor authorization changed; retry"}
	}
	return nil
}
func (c *Client) Start(ctx context.Context, owner string) (Authorization, error) {
	if !c.Enabled() || owner == "" {
		return Authorization{}, errors.New("cursor authorization unavailable")
	}
	login, err := NewOAuthLogin()
	if err != nil {
		return Authorization{}, err
	}
	session := authorizationSession{Owner: owner, Login: login, Authorization: Authorization{
		ID: login.ID, State: "pending", URL: login.URL, ExpiresAt: time.Now().UTC().Add(authorizationTTL),
	}}
	raw, err := json.Marshal(session)
	if err != nil {
		return Authorization{}, err
	}
	key, err := authKey(login.ID)
	if err != nil {
		return Authorization{}, err
	}
	if ok, err := c.rdb.SetNX(ctx, key, raw, authorizationTTL).Result(); err != nil || !ok {
		return Authorization{}, errors.New("cursor authorization store unavailable")
	}
	return session.Authorization, nil
}
func (c *Client) Status(ctx context.Context, owner, id string) (Authorization, error) {
	session, old, err := c.load(ctx, owner, id)
	if err != nil {
		return Authorization{}, err
	}
	if session.State != "pending" {
		return session.Authorization, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	credentials, err := PollOAuth(ctx, session.Login, c.do)
	if err != nil {
		return Authorization{}, err
	}
	if credentials == nil {
		return session.Authorization, nil
	}
	models, err := OAuthModels(ctx, credentials.AccessToken, c.do)
	if err != nil {
		return Authorization{}, err
	}
	session.State, session.Models = "authorized", models
	session.KeyExpiresAt, session.AccessToken = credentials.ExpiresAt, credentials.AccessToken
	session.Login = OAuthLogin{}
	if err := c.replace(ctx, id, old, &session); err != nil {
		return Authorization{}, err
	}
	return session.Authorization, nil
}
func (c *Client) Cancel(ctx context.Context, owner, id string) error {
	session, old, err := c.load(ctx, owner, id)
	if err != nil {
		return err
	}
	if session.Claim != "" {
		return &Error{409, "Cursor account import is in progress"}
	}
	return c.replace(ctx, id, old, nil)
}
func (c *Client) Claim(ctx context.Context, owner, id string) (CredentialClaim, error) {
	session, old, err := c.load(ctx, owner, id)
	if err != nil {
		return CredentialClaim{}, err
	}
	if session.State != "authorized" || session.Claim != "" || !session.KeyExpiresAt.After(time.Now()) {
		return CredentialClaim{}, &Error{409, "Cursor authorization is not available for import"}
	}
	session.Claim = uuid.NewString()
	if err := c.replace(ctx, id, old, &session); err != nil {
		return CredentialClaim{}, err
	}
	return CredentialClaim{Authorization: session.Authorization, APIKey: session.AccessToken, Claim: session.Claim}, nil
}
func (c *Client) Settle(ctx context.Context, owner, id, claim string, success bool) error {
	session, old, err := c.load(ctx, owner, id)
	if err != nil {
		return err
	}
	if claim == "" || session.Claim != claim {
		return &Error{409, "Invalid Cursor import claim"}
	}
	if success {
		return c.replace(ctx, id, old, nil)
	}
	session.Claim = ""
	return c.replace(ctx, id, old, &session)
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
