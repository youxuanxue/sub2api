package qa

import "time"

type RawSSEChunk struct {
	Bytes    []byte `json:"bytes"`
	RecvAtMs int64  `json:"recv_at_ms"`
}

type CaptureInput struct {
	RequestID         string
	UserID            int64
	APIKeyID          int64
	AccountID         *int64
	Platform          string
	RequestedModel    string
	UpstreamModel     string
	InboundEndpoint   string
	UpstreamEndpoint  string
	StatusCode        int
	DurationMs        int64
	FirstTokenMs      *int64
	Stream            bool
	RequestBody       []byte
	ResponseBody      []byte
	ResponseHeaders   map[string]string
	StreamChunks      []RawSSEChunk
	InputTokens       int
	OutputTokens      int
	CachedTokens      int
	ToolCallsPresent  bool
	MultimodalPresent bool
	Tags              []string
	CreatedAt         time.Time
}

type ExportResult struct {
	Key         string `json:"key"`
	DownloadURL string `json:"download_url"`
}
