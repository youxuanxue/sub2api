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
