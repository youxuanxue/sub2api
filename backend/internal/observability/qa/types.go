package qa

import "time"

type RawSSEChunk struct {
	Bytes    []byte `json:"bytes"`
	RecvAtMs int64  `json:"recv_at_ms"`
}

type CaptureInput struct {
	RequestID        string
	TrajectoryID     string
	UserID           int64
	GroupID          *int64
	APIKeyID         int64
	AccountID        *int64
	Platform         string
	Provider         string
	ChannelType      *int
	RequestedModel   string
	UpstreamModel    string
	InboundEndpoint  string
	UpstreamEndpoint string
	StatusCode       int
	Success          bool
	DurationMs       int64
	FirstTokenMs     *int64
	Stream           bool
	RequestBody      []byte
	// UpstreamRequestBody 仅在 traj/synth opt-in 且网关真的改写过请求体时非空
	//（转发到上游的最终请求与客户端原始请求字节不等）。进 blob 的
	// request.upstream_body + request.upstream_divergent，供导出侧标记失真记录。
	UpstreamRequestBody        []byte
	ResponseBody               []byte
	ResponseHeaders            map[string]string
	StreamChunks               []RawSSEChunk
	InputTokens                int
	OutputTokens               int
	CachedTokens               int
	ToolCallsPresent           bool
	MultimodalPresent          bool
	RequestBlobURI             string
	ResponseBlobURI            string
	StreamBlobURI              string
	RedactionVersion           string
	CaptureStatus              string
	Tags                       []string
	CreatedAt                  time.Time
	InternalThinkingBlocksJSON []string

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
	StorageKey  string    `json:"-"`
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
	// APIKeyID, when non-nil, restricts the export to records produced by a
	// single API key (TK per-key "导出对话记录"). Combined as AND with the
	// user_id scope, so a foreign key id simply yields zero rows.
	APIKeyID *int64
	// Platform, when non-empty, restricts the export to one platform. The traj
	// v2 projector only faithfully reconstructs Anthropic /v1/messages shapes,
	// so the traj export pins this to "anthropic"; non-anthropic records (whose
	// blobs would project to empty/garbage turns) are excluded.
	Platform string
	// Format selects the export shape: "" / "v1" = legacy per-message
	// ExportRow JSONL; "v2" = richer session/turns (traj v2, .examples-aligned,
	// one TrajSessionV2 object per line, carries thinking/signature/usage).
	Format string
}
