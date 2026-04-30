package newapi

import (
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// NormalizeArkChannelBaseURL trims mistaken path suffixes from Volcengine Ark-style
// channel base URLs. Admins often paste the full chat endpoint or /api/v3 root from
// docs; new-api adaptors append /api/v3/... themselves, so a base like
// https://ark.cn-beijing.volces.com/api/v3 would become a broken double path.
func NormalizeArkChannelBaseURL(channelType int, base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return base
	}
	base = strings.TrimRight(base, "/")
	switch channelType {
	case newapiconstant.ChannelTypeVolcEngine, newapiconstant.ChannelTypeDoubaoVideo:
		for _, suf := range []string{
			"/api/v3/chat/completions",
			"/api/v3/bots/chat/completions",
			"/api/v3/models",
			"/api/v3",
		} {
			if strings.HasSuffix(base, suf) {
				return strings.TrimRight(strings.TrimSuffix(base, suf), "/")
			}
		}
	}
	return base
}
