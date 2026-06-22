package httputil

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestValidatePublicDownloadURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"https public", "https://cdn.example.com/video.mp4?sig=1", true},
		{"http public allowed", "http://cdn.example.com/video.mp4", true},
		{"javascript rejected", "javascript:alert(1)", false},
		{"userinfo rejected", "https://user:pass@cdn.example.com/video.mp4", false},
		{"localhost rejected", "https://localhost/video.mp4", false},
		{"localhost suffix rejected", "https://foo.localhost/video.mp4", false},
		{"loopback rejected", "https://127.0.0.1/video.mp4", false},
		{"private rejected", "https://10.0.0.1/video.mp4", false},
		{"metadata rejected", "http://169.254.169.254/latest/meta-data", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validatePublicDownloadURL(tc.raw)
			if tc.ok && err != nil {
				t.Fatalf("expected valid URL, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected URL validation error")
			}
		})
	}
}

func TestDownloadPublicURLRejectsLocalhostBeforeRequest(t *testing.T) {
	_, err := DownloadPublicURL(context.Background(), "http://127.0.0.1/video.mp4", 4, time.Second)
	if err == nil || !strings.Contains(err.Error(), "host is not allowed") {
		t.Fatalf("expected host rejection, got %v", err)
	}
}

func TestReadPublicDownloadResponseRejectsOversizedContentLength(t *testing.T) {
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: 10,
		Body:          io.NopCloser(strings.NewReader("0123456789")),
	}
	_, err := readPublicDownloadResponse(resp, 4)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected too-large error, got %v", err)
	}
}

func TestReadPublicDownloadResponseRejectsOversizedChunkedBody(t *testing.T) {
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: -1,
		Body:          io.NopCloser(strings.NewReader("0123456789")),
	}
	_, err := readPublicDownloadResponse(resp, 4)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected too-large error, got %v", err)
	}
}

func TestReadPublicDownloadResponseReturnsBodyAndContentType(t *testing.T) {
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		ContentLength: 3,
		Header:        http.Header{"Content-Type": []string{"video/webm"}},
		Body:          io.NopCloser(strings.NewReader("abc")),
	}
	got, err := readPublicDownloadResponse(resp, 4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got.Body) != "abc" || got.ContentType != "video/webm" {
		t.Fatalf("unexpected download: body=%q contentType=%q", got.Body, got.ContentType)
	}
}
