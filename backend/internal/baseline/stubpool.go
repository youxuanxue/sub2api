package baseline

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sync"
)

// StubPoolBaselineDoc mirrors anthropic-stub-pool-baselines.json.
type StubPoolBaselineDoc struct {
	SchemaVersion int `json:"schema_version"`
	Policy        struct {
		BaseURLPattern     string `json:"base_url_pattern"`
		Platform           string `json:"platform"`
		AccountType        string `json:"account_type"`
		PoolModeEnabled    bool   `json:"pool_mode_enabled"`
		PoolModeRetryCount int    `json:"pool_mode_retry_count"`
	} `json:"policy"`
}

var (
	stubPoolOnce    sync.Once
	stubPoolDoc     *StubPoolBaselineDoc
	stubPoolRegexp  *regexp.Regexp
	stubPoolLoadErr error
)

// LoadStubPoolBaseline parses the embedded stub-pool policy once and caches it,
// pre-compiling the base_url match regexp.
func LoadStubPoolBaseline() (*StubPoolBaselineDoc, *regexp.Regexp, error) {
	stubPoolOnce.Do(func() {
		doc := &StubPoolBaselineDoc{}
		if err := json.Unmarshal(stubPoolBaselineJSON, doc); err != nil {
			stubPoolLoadErr = fmt.Errorf("parse embedded stub-pool baseline: %w", err)
			return
		}
		if doc.Policy.BaseURLPattern == "" {
			stubPoolLoadErr = fmt.Errorf("embedded stub-pool baseline has empty base_url_pattern")
			return
		}
		re, err := regexp.Compile(doc.Policy.BaseURLPattern)
		if err != nil {
			stubPoolLoadErr = fmt.Errorf("compile stub-pool base_url_pattern %q: %w", doc.Policy.BaseURLPattern, err)
			return
		}
		stubPoolDoc = doc
		stubPoolRegexp = re
	})
	return stubPoolDoc, stubPoolRegexp, stubPoolLoadErr
}
