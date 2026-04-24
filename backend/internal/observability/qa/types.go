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

	// issue #59 Gap 2: synthetic-pipeline tagging headers.
	// X-Synth-Pipeline is only used to compute DialogSynth (no schema
	// column for the pipeline name itself), so it isn't carried here.
	SynthSessionID     string
	SynthRole          string
	SynthEngineerLevel string
	DialogSynth        bool
}

type ExportResult struct {
	DownloadURL string    `json:"download_url"`
	ExpiresAt   time.Time `json:"expires_at"`
	RecordCount int       `json:"record_count"`
}

// ExportFilter narrows the qa_records covered by an export run. Zero-value
// fields mean "no filter on this dimension". When SynthSessionID is set,
// it takes precedence and overrides Since/Until (the M0 client wants the
// full session even if it spans the default 24h window).
type ExportFilter struct {
	// Since / Until are inclusive bounds on created_at. Both zero ⇒
	// no time bound. Ignored when SynthSessionID is set.
	Since          time.Time
	Until          time.Time
	SynthSessionID string
	SynthRole      string
}
