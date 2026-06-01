//go:build unit

package service

import "testing"

// openaiChallengeBodySnippet is a trimmed copy of the real OpenAI/Cloudflare anti-bot
// interstitial captured from prod (openai_privacy_failed status=403). It is the OpenAI
// branded challenge page (gray logo SVG + meta-refresh) and contains NONE of the legacy
// "cloudflare" / "cf-" / "Just a moment" markers — that gap caused the false "Fail".
const openaiChallengeBodySnippet = `<html>
  <head>
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <style global>body{font-family:Arial,Helvetica,sans-serif}.logo{color:#8e8ea0}</style>
  <meta http-equiv="refresh" content="360"></head>
  <body>
    <div class="container">
      <div class="logo">
        <svg width="41" height="41" viewBox="0 0 41 41" fill="none">
          <path d="M37.5324 16.8707C37.9808 15.5241 38.1363 14.0974"/>
        </svg>
      </div>
    </div>
  </body>
</html>`

func TestClassifyOpenAIPrivacyResponse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		status      int
		contentType string
		body        string
		want        string
	}{
		{
			name:        "2xx json success",
			status:      200,
			contentType: "application/json",
			body:        `{"success":true}`,
			want:        PrivacyModeTrainingOff,
		},
		{
			name:        "403 openai branded challenge page",
			status:      403,
			contentType: "text/html; charset=utf-8",
			body:        openaiChallengeBodySnippet,
			want:        PrivacyModeCFBlocked,
		},
		{
			name:        "503 classic cloudflare just a moment",
			status:      503,
			contentType: "text/html",
			body:        `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body>cloudflare</body></html>`,
			want:        PrivacyModeCFBlocked,
		},
		{
			name:        "403 genuine json error stays failed",
			status:      403,
			contentType: "application/json",
			body:        `{"detail":"missing scope"}`,
			want:        PrivacyModeFailed,
		},
		{
			name:        "400 json bad request stays failed",
			status:      400,
			contentType: "application/json",
			body:        `{"detail":"invalid feature"}`,
			want:        PrivacyModeFailed,
		},
		{
			name:        "403 html content-type empty body still cf",
			status:      403,
			contentType: "text/html",
			body:        "",
			want:        PrivacyModeCFBlocked,
		},
		{
			name:        "403 meta-refresh body without content-type header",
			status:      403,
			contentType: "",
			body:        `<html><head><meta http-equiv="refresh" content="360"></head></html>`,
			want:        PrivacyModeCFBlocked,
		},
		{
			name:        "500 json failure stays failed",
			status:      500,
			contentType: "application/json",
			body:        `{"error":"internal"}`,
			want:        PrivacyModeFailed,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyOpenAIPrivacyResponse(tc.status, tc.contentType, tc.body); got != tc.want {
				t.Fatalf("classifyOpenAIPrivacyResponse(%d, %q, ...) = %q, want %q", tc.status, tc.contentType, got, tc.want)
			}
		})
	}
}
