package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
)

const (
	antigravityRemoteImageMaxBytes  = 15 << 20
	antigravityRemoteImageMaxCount  = 8
	antigravityRemoteImageTimeout   = 20 * time.Second
	antigravityRemoteImageRedirects = 5
)

// antigravityRemoteImageHTTPClient fetches public http(s) images for Antigravity
// inlining. Dial is SSRF-safe (no private/loopback/metadata targets).
var antigravityRemoteImageHTTPClient = newAntigravityRemoteImageHTTPClient(antigravityRemoteImageTimeout)

func newAntigravityRemoteImageHTTPClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		DialContext:           safeDialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: servertiming.WrapRoundTripper(tr),
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= antigravityRemoteImageRedirects {
			return fmt.Errorf("too many redirects")
		}
		if err := antigravityRemoteImageURLAllowed(req.Context(), req.URL.String()); err != nil {
			return err
		}
		return nil
	}
	return client
}

type antigravityFetchedImage struct {
	Mime string
	Data []byte
}

// fetchAntigravityRemoteImage downloads one public image URL. Tests replace this.
var fetchAntigravityRemoteImage = defaultFetchAntigravityRemoteImage

func defaultFetchAntigravityRemoteImage(ctx context.Context, rawURL string) (antigravityFetchedImage, error) {
	var zero antigravityFetchedImage
	if err := antigravityRemoteImageURLAllowed(ctx, rawURL); err != nil {
		return zero, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("Accept", "image/*,*/*;q=0.8")
	resp, err := antigravityRemoteImageHTTPClient.Do(req)
	if err != nil {
		return zero, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return zero, fmt.Errorf("image HTTP %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, antigravityRemoteImageMaxBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return zero, err
	}
	if len(raw) > antigravityRemoteImageMaxBytes {
		return zero, fmt.Errorf("image exceeds %d bytes", antigravityRemoteImageMaxBytes)
	}
	mime := antigravitySniffImageMIME(resp.Header.Get("Content-Type"), raw)
	if mime == "" {
		return zero, fmt.Errorf("response is not a supported image")
	}
	return antigravityFetchedImage{Mime: mime, Data: raw}, nil
}

func antigravityRemoteImageURLAllowed(ctx context.Context, rawURL string) error {
	u, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil || u.URL == nil {
		return fmt.Errorf("invalid image URL")
	}
	scheme := strings.ToLower(u.URL.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("image URL scheme must be http or https")
	}
	host := u.URL.Hostname()
	if host == "" {
		return fmt.Errorf("image URL host is empty")
	}
	blocked, err := isPrivateOrLoopbackHost(ctx, host)
	if err != nil {
		return err
	}
	if blocked {
		return fmt.Errorf("image URL host is not publicly reachable")
	}
	return nil
}

func antigravitySniffImageMIME(contentType string, body []byte) string {
	if mime := antigravityNormalizeImageMIME(contentType); mime != "" {
		return mime
	}
	if len(body) == 0 {
		return ""
	}
	detected := http.DetectContentType(body)
	return antigravityNormalizeImageMIME(detected)
}

func antigravityNormalizeImageMIME(raw string) string {
	mime := strings.ToLower(strings.TrimSpace(strings.Split(raw, ";")[0]))
	switch mime {
	case "image/jpg":
		return "image/jpeg"
	case "image/png", "image/jpeg", "image/webp", "image/gif", "image/heic", "image/heif":
		return mime
	default:
		return ""
	}
}

func antigravityDeclaredImageMIME(raw string) bool {
	mime := strings.ToLower(strings.TrimSpace(strings.Split(raw, ";")[0]))
	if mime == "" || mime == "image/*" {
		return true
	}
	return antigravityNormalizeImageMIME(mime) != ""
}

// antigravityPrepareRemoteMedia downloads public http(s) image fileUri parts into
// inlineData for Antigravity, then rejects any remaining remote fileUri (gs://,
// non-image URL, etc.). Official Gemini accounts are unchanged.
func antigravityPrepareRemoteMedia(ctx context.Context, account *Account, action string, body []byte) ([]byte, error) {
	if account == nil || account.Platform != PlatformAntigravity {
		return body, nil
	}
	if action != "generateContent" && action != "streamGenerateContent" {
		return body, nil
	}
	if !GeminiBodyHasRemoteFileURI(body) {
		return body, nil
	}
	rewritten, err := inlineAntigravityRemoteImages(ctx, body)
	if err != nil {
		return nil, err
	}
	if GeminiBodyHasRemoteFileURI(rewritten) {
		return nil, &AntigravityInlineMediaError{}
	}
	return rewritten, nil
}

func inlineAntigravityRemoteImages(ctx context.Context, body []byte) ([]byte, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, &AntigravityInlineMediaError{Reason: "Request body must be a JSON object"}
	}
	cache := make(map[string]antigravityFetchedImage)
	var count int
	if err := walkInlineAntigravityRemoteImages(ctx, root, cache, &count); err != nil {
		return nil, err
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func walkInlineAntigravityRemoteImages(ctx context.Context, node any, cache map[string]antigravityFetchedImage, count *int) error {
	switch typed := node.(type) {
	case map[string]any:
		if err := maybeInlineAntigravityFileDataPart(ctx, typed, cache, count); err != nil {
			return err
		}
		for _, value := range typed {
			if err := walkInlineAntigravityRemoteImages(ctx, value, cache, count); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := walkInlineAntigravityRemoteImages(ctx, item, cache, count); err != nil {
				return err
			}
		}
	}
	return nil
}

func maybeInlineAntigravityFileDataPart(ctx context.Context, part map[string]any, cache map[string]antigravityFetchedImage, count *int) error {
	fileKey, file, ok := antigravityFileDataObject(part)
	if !ok {
		return nil
	}
	uri := antigravityFileDataURI(file)
	if !geminiRemoteFileURI(uri) {
		return nil
	}
	low := strings.ToLower(uri)
	if strings.HasPrefix(low, "gs://") {
		// Leave in place; prepare step rejects remaining remotes.
		return nil
	}
	if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
		return nil
	}
	declared := antigravityFileDataMIME(file)
	if !antigravityDeclaredImageMIME(declared) {
		return nil
	}
	if *count >= antigravityRemoteImageMaxCount {
		return &AntigravityInlineMediaError{Reason: "too many remote images for Antigravity inline"}
	}
	fetched, ok := cache[uri]
	if !ok {
		img, err := fetchAntigravityRemoteImage(ctx, uri)
		if err != nil {
			return &AntigravityInlineMediaError{Reason: "invalid remote image URL for Antigravity; use inlineData"}
		}
		fetched = img
		cache[uri] = fetched
	}
	*count++
	delete(part, fileKey)
	part["inlineData"] = map[string]any{
		"mimeType": fetched.Mime,
		"data":     base64.StdEncoding.EncodeToString(fetched.Data),
	}
	return nil
}

func antigravityFileDataObject(part map[string]any) (key string, file map[string]any, ok bool) {
	for _, candidate := range []string{"fileData", "file_data"} {
		raw, exists := part[candidate]
		if !exists {
			continue
		}
		file, ok = raw.(map[string]any)
		if ok {
			return candidate, file, true
		}
	}
	return "", nil, false
}

func antigravityFileDataURI(file map[string]any) string {
	for _, key := range []string{"fileUri", "file_uri"} {
		if raw, ok := file[key].(string); ok {
			if uri := strings.TrimSpace(raw); uri != "" {
				return uri
			}
		}
	}
	return ""
}

func antigravityFileDataMIME(file map[string]any) string {
	for _, key := range []string{"mimeType", "mime_type"} {
		if raw, ok := file[key].(string); ok {
			return raw
		}
	}
	return ""
}
