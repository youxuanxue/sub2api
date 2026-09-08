package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	cursorbridge "github.com/Wei-Shaw/sub2api/internal/integration/cursor"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const cursorRelayOwnerHeader = "X-TokenKey-Cursor-Owner"
const cursorRelayTimeHeader = "X-TokenKey-Cursor-Time"
const cursorRelaySignatureHeader = "X-TokenKey-Cursor-Signature"

func cursorContinuationInRequest(request protocolrouter.CanonicalRequest) bool {
	body := gjson.ParseBytes(request.Body())
	items := body.Get("messages").Array()
	if request.InboundProtocol() == protocolrouter.ProtocolResponses {
		items = body.Get("input").Array()
	}
	// Only the current tool-result turn resumes a parked run. Prior results and
	// fields inside tool arguments or metadata do not constrain a new request.
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		switch request.InboundProtocol() {
		case protocolrouter.ProtocolMessages:
			if item.Get("role").String() != "user" {
				continue
			}
			for _, block := range item.Get("content").Array() {
				if block.Get("type").String() == "tool_result" && strings.HasPrefix(block.Get("tool_use_id").String(), "toolu_bf_") {
					return true
				}
			}
			return false
		case protocolrouter.ProtocolChatCompletions:
			if item.Get("role").String() != "tool" {
				return false
			}
			if strings.HasPrefix(item.Get("tool_call_id").String(), "toolu_bf_") {
				return true
			}
		case protocolrouter.ProtocolResponses:
			if item.Get("type").String() != "function_call_output" {
				return false
			}
			if strings.HasPrefix(item.Get("call_id").String(), "toolu_bf_") {
				return true
			}
		}
	}
	return false
}

func validateCursorBridgeBaseURL(account *Account, raw string, fallback func(string) (string, error)) (string, error) {
	if !account.IsCursor() || isEdgeMirrorStub(account, edgeIDPattern) {
		return fallback(raw)
	}
	client, err := cursorbridge.FromEnv()
	if err != nil {
		return "", err
	}
	if strings.TrimRight(strings.TrimSpace(raw), "/") != client.BaseURL() {
		return "", errors.New("cursor endpoint does not match configured bridge")
	}
	return client.BaseURL(), nil
}

func cursorModelParameters(account *Account, model string) ([]cursorbridge.Parameter, bool) {
	if !account.IsCursor() {
		return nil, false
	}
	all, ok := account.Credentials[CursorModelParametersKey].(map[string]any)
	if !ok {
		return nil, false
	}
	value, ok := all[model]
	if !ok {
		return nil, false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var params []cursorbridge.Parameter
	if json.Unmarshal(raw, &params) != nil {
		return nil, false
	}
	return params, true
}

func cursorMappedModelAllowed(account *Account, requested, resolved string) bool {
	_, mapped := account.ResolveMappedModel(requested)
	_, known := cursorModelParameters(account, resolved)
	return mapped && known
}

func cursorRelaySignature(secret, owner, timestamp, credential string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fingerprint := sha256.Sum256([]byte(credential))
	fmt.Fprintf(mac, "%s\n%s\n%x", owner, timestamp, fingerprint)
	return hex.EncodeToString(mac.Sum(nil))
}

func cursorTrustedOwner(c *gin.Context, account *Account) (string, error) {
	if c == nil {
		return fmt.Sprintf("probe:account:%d", account.ID), nil
	}
	v, exists := c.Get("api_key")
	key, ok := v.(*APIKey)
	if !exists || !ok || key == nil || key.ID <= 0 || key.UserID <= 0 {
		return "", errors.New("cursor requires authenticated user and API key identity")
	}
	owner := fmt.Sprintf("user:%d:key:%d", key.UserID, key.ID)
	if c.GetHeader(cursorRelayOwnerHeader) != "" {
		secret := os.Getenv("CURSOR_RELAY_SECRET")
		if len(secret) < 32 {
			return "", errors.New("cursor relay identity is not configured")
		}
		owner = c.GetHeader(cursorRelayOwnerHeader)
		timestamp := c.GetHeader(cursorRelayTimeHeader)
		seconds, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil || time.Since(time.Unix(seconds, 0)).Abs() > 5*time.Minute || len(owner) > 150 {
			return "", errors.New("invalid Cursor relay identity")
		}
		credential := ""
		if scheme, value, found := strings.Cut(c.GetHeader("Authorization"), " "); found && strings.EqualFold(scheme, "Bearer") {
			credential = strings.TrimSpace(value)
		}
		if credential == "" {
			credential = c.GetHeader("x-api-key")
		}
		want := cursorRelaySignature(secret, owner, timestamp, credential)
		if !hmac.Equal([]byte(want), []byte(c.GetHeader(cursorRelaySignatureHeader))) {
			return "", errors.New("invalid Cursor relay signature")
		}
		owner = "relay:" + strconv.FormatInt(key.ID, 10) + ":" + owner
	}
	return owner, nil
}

// prepareCursorUpstreamRequest is shared by Messages, Chat/Responses conversion
// and endpoint probing. It overwrites all client-supplied internal identity.
func prepareCursorUpstreamRequest(req *http.Request, c *gin.Context, account *Account) error {
	if !account.IsCursor() {
		return nil
	}
	owner, err := cursorTrustedOwner(c, account)
	if err != nil {
		return err
	}
	req.Header.Del(cursorbridge.SecretHeader)
	req.Header.Del(cursorbridge.TenantHeader)
	if isEdgeMirrorStub(account, edgeIDPattern) {
		secret := os.Getenv("CURSOR_RELAY_SECRET")
		if len(secret) < 32 {
			return errors.New("cursor relay secret is not configured")
		}
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		req.Header.Set(cursorRelayOwnerHeader, owner)
		req.Header.Set(cursorRelayTimeHeader, timestamp)
		req.Header.Set(cursorRelaySignatureHeader, cursorRelaySignature(secret, owner, timestamp, account.GetCredential("api_key")))
		return nil
	}
	client, err := cursorbridge.FromEnv()
	if err != nil {
		return err
	}
	if req.URL.String() != client.BaseURL()+"/v1/messages" {
		return errors.New("cursor account endpoint does not match the configured bridge")
	}
	if req.Body == nil {
		return errors.New("missing Cursor request body")
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, (16<<20)+1))
	if err != nil || len(body) > 16<<20 {
		return errors.New("invalid Cursor request body")
	}
	_ = req.Body.Close()
	model := gjson.GetBytes(body, "model").String()
	params, ok := cursorModelParameters(account, model)
	if !ok {
		return errors.New("cursor model is not in the account catalog")
	}
	if params == nil {
		params = []cursorbridge.Parameter{}
	}
	body, err = sjson.SetBytes(body, "cursor_model", map[string]any{"id": model, "params": params})
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	client.SetHeaders(req.Header, owner+fmt.Sprintf(":account:%d", account.ID))
	return nil
}
