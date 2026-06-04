package baseline

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// HTTPMimicryBaseline mirrors anthropic-http-mimicry-baselines.json: the canonical
// Claude Code OAuth UA version + per-model anthropic-beta token lists. It is the
// single source of truth the per-node config reconciler self-heals the
// settings.claude_code_user_agent_version / claude_code_http_mimicry_manifest
// runtime knobs toward, so a fresh node acquires the canonical UA without an
// operator sync-runtime round-trip.
type HTTPMimicryBaseline struct {
	SchemaVersion int      `json:"schema_version"`
	CCVersion     string   `json:"cc_version"`
	SonnetOpus    []string `json:"sonnet_opus"`
	Haiku         []string `json:"haiku"`
}

var (
	mimicryDocOnce sync.Once
	mimicryDoc     *HTTPMimicryBaseline
	mimicryDocErr  error
)

// LoadHTTPMimicryBaseline parses the embedded HTTP mimicry baseline once and caches it.
func LoadHTTPMimicryBaseline() (*HTTPMimicryBaseline, error) {
	mimicryDocOnce.Do(func() {
		doc := &HTTPMimicryBaseline{}
		if err := json.Unmarshal(httpMimicryBaselineJSON, doc); err != nil {
			mimicryDocErr = fmt.Errorf("parse embedded http mimicry baseline: %w", err)
			return
		}
		if doc.SchemaVersion < 1 {
			mimicryDocErr = fmt.Errorf("embedded http mimicry baseline schema_version < 1")
			return
		}
		if strings.TrimSpace(doc.CCVersion) == "" {
			mimicryDocErr = fmt.Errorf("embedded http mimicry baseline cc_version is empty")
			return
		}
		if len(doc.SonnetOpus) == 0 || len(doc.Haiku) == 0 {
			mimicryDocErr = fmt.Errorf("embedded http mimicry baseline has empty sonnet_opus/haiku")
			return
		}
		mimicryDoc = doc
	})
	return mimicryDoc, mimicryDocErr
}
