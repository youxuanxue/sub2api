//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// imgFakeStore records uploads and returns deterministic presigned URLs.
type imgFakeStore struct {
	uploads    map[string][]byte
	uploadErr  error
	presignErr error
}

func (f *imgFakeStore) Upload(_ context.Context, key string, body []byte, _ string) error {
	if f.uploadErr != nil {
		return f.uploadErr
	}
	if f.uploads == nil {
		f.uploads = map[string][]byte{}
	}
	f.uploads[key] = append([]byte(nil), body...)
	return nil
}

func (f *imgFakeStore) PresignURL(_ context.Context, key string, _ time.Duration) (string, error) {
	if f.presignErr != nil {
		return "", f.presignErr
	}
	return "https://s3.example.test/" + key, nil
}

var _ MediaStore = (*imgFakeStore)(nil)

// imagesBodyWith builds an images-generations success body from per-item field maps.
func imagesBodyWith(items ...map[string]string) []byte {
	body := []byte(`{"created":1,"data":[]}`)
	for _, fields := range items {
		item := []byte(`{}`)
		for k, v := range fields {
			item, _ = sjson.SetBytes(item, k, v)
		}
		body, _ = sjson.SetRawBytes(body, "data.-1", item)
	}
	return body
}

func TestMaybeOffloadImagesToS3_OffloadsDataURI(t *testing.T) {
	raw := []byte("fake-png-bytes-\x00\x01\x02")
	b64 := base64.StdEncoding.EncodeToString(raw)
	fs := &imgFakeStore{}
	svc := &OpenAIGatewayService{mediaStore: fs}

	body := imagesBodyWith(map[string]string{
		"url":            "data:image/png;base64," + b64,
		"revised_prompt": "a cat",
	})
	out := svc.tkMaybeOffloadImagesToS3(context.Background(), body, "")

	url := gjson.GetBytes(out, "data.0.url").String()
	if url == "" || url[:8] != "https://" {
		t.Fatalf("expected presigned https url, got %q", url)
	}
	if gjson.GetBytes(out, "data.0.b64_json").Exists() {
		t.Error("b64_json must be stripped after offload")
	}
	key := gjson.GetBytes(out, "data.0.s3_key").String()
	if key == "" || key[:len(MediaImageKeyPrefix)] != MediaImageKeyPrefix {
		t.Fatalf("expected s3_key under %q, got %q", MediaImageKeyPrefix, key)
	}
	if gjson.GetBytes(out, "data.0.revised_prompt").String() != "a cat" {
		t.Error("revised_prompt must be preserved")
	}
	if got, ok := fs.uploads[key]; !ok || string(got) != string(raw) {
		t.Errorf("uploaded bytes mismatch for key %q: ok=%v", key, ok)
	}
	// Key is content-addressed: extension follows the data: URI mime.
	if key[len(key)-4:] != ".png" {
		t.Errorf("expected .png extension, got key %q", key)
	}
}

func TestMaybeOffloadImagesToS3_OffloadsBareB64JSON(t *testing.T) {
	// Default response (no response_format): the OAuth builder emits a b64_json
	// field, NOT a data: URI. Offload must still engage — no client opt-in needed.
	raw := []byte("bare-b64-image")
	b64 := base64.StdEncoding.EncodeToString(raw)
	fs := &imgFakeStore{}
	svc := &OpenAIGatewayService{mediaStore: fs}

	out := svc.tkMaybeOffloadImagesToS3(context.Background(), imagesBodyWith(map[string]string{"b64_json": b64}), "")

	if got := gjson.GetBytes(out, "data.0.url").String(); got[:8] != "https://" {
		t.Fatalf("bare b64_json should be offloaded to a presigned url, got %q", got)
	}
	if gjson.GetBytes(out, "data.0.b64_json").Exists() {
		t.Error("b64_json must be stripped after offload")
	}
	if len(fs.uploads) != 1 || string(firstUpload(fs)) != string(raw) {
		t.Error("uploaded bytes mismatch for bare b64_json")
	}
}

func TestMaybeOffloadImagesToS3_Passthrough(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("x"))
	cases := []struct {
		name   string
		store  MediaStore
		format string
		body   []byte
	}{
		{
			name:  "nil store disabled",
			store: nil,
			body:  imagesBodyWith(map[string]string{"url": "data:image/png;base64," + b64}),
		},
		{
			name:  "already an http url",
			store: &imgFakeStore{},
			body:  imagesBodyWith(map[string]string{"url": "https://cdn.example/img.png"}),
		},
		{
			name:   "explicit response_format=b64_json honoured",
			store:  &imgFakeStore{},
			format: "b64_json",
			body:   imagesBodyWith(map[string]string{"url": "data:image/png;base64," + b64}),
		},
		{
			name:  "non-base64 data uri",
			store: &imgFakeStore{},
			body:  imagesBodyWith(map[string]string{"url": "data:image/png,notbase64"}),
		},
		{
			name:  "no data array",
			store: &imgFakeStore{},
			body:  []byte(`{"created":1}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{mediaStore: tc.store}
			out := svc.tkMaybeOffloadImagesToS3(context.Background(), tc.body, tc.format)
			if string(out) != string(tc.body) {
				t.Errorf("expected body unchanged, got %s", out)
			}
		})
	}
}

// firstUpload returns the single uploaded blob (test helper; expects exactly one).
func firstUpload(fs *imgFakeStore) []byte {
	for _, v := range fs.uploads {
		return v
	}
	return nil
}

func TestMaybeOffloadImagesToS3_BestEffortOnStoreError(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("x"))
	body := imagesBodyWith(map[string]string{"url": "data:image/png;base64," + b64})

	for _, tc := range []struct {
		name  string
		store *imgFakeStore
	}{
		{"upload error", &imgFakeStore{uploadErr: errors.New("boom")}},
		{"presign error", &imgFakeStore{presignErr: errors.New("boom")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{mediaStore: tc.store}
			out := svc.tkMaybeOffloadImagesToS3(context.Background(), body, "")
			// Never fail a billed generation over offload — original data: URI survives.
			if gjson.GetBytes(out, "data.0.url").String() != "data:image/png;base64,"+b64 {
				t.Errorf("expected original data: URI preserved on store error, got %s", out)
			}
		})
	}
}

func TestMaybeOffloadImagesToS3_MixedItems(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("img"))
	fs := &imgFakeStore{}
	svc := &OpenAIGatewayService{mediaStore: fs}
	body := imagesBodyWith(
		map[string]string{"url": "data:image/jpeg;base64," + b64}, // offload → .jpg
		map[string]string{"url": "https://cdn.example/keep.png"},  // passthrough
	)
	out := svc.tkMaybeOffloadImagesToS3(context.Background(), body, "")

	if got := gjson.GetBytes(out, "data.0.url").String(); got[:8] != "https://" || got[len(got)-4:] != ".jpg" {
		t.Errorf("item 0 should be offloaded to a .jpg presigned url, got %q", got)
	}
	if got := gjson.GetBytes(out, "data.1.url").String(); got != "https://cdn.example/keep.png" {
		t.Errorf("item 1 (http url) should be untouched, got %q", got)
	}
	if len(fs.uploads) != 1 {
		t.Errorf("expected exactly 1 upload, got %d", len(fs.uploads))
	}
}
