package trajectory

import "github.com/Wei-Shaw/sub2api/ent"

type EvidenceBlob struct {
	Request struct {
		Path string `json:"path"`
		Body any    `json:"body"`
		// UpstreamBody / UpstreamDivergent：网关在转发前改写过请求体时（仅
		// traj/synth opt-in 记录捕获），这里携带真正发往上游的最终请求体。
		// divergent=true 表示「客户端视角的 body ≠ 产生该响应的真实请求」。
		UpstreamBody      any  `json:"upstream_body,omitempty"`
		UpstreamDivergent bool `json:"upstream_divergent,omitempty"`
	} `json:"request"`
	Response struct {
		StatusCode             int            `json:"status_code"`
		Headers                map[string]any `json:"headers"`
		Body                   any            `json:"body"`
		InternalThinkingBlocks []any          `json:"internal_thinking_blocks,omitempty"`
	} `json:"response"`
	Stream struct {
		Chunks []map[string]any `json:"chunks"`
	} `json:"stream"`
}

type SourceRecord struct {
	Record *ent.QARecord
	Blob   *EvidenceBlob
}

type ExportSummary struct {
	RecordCount     int
	SessionCount    int
	ToolCallCount   int
	ToolResultCount int
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
