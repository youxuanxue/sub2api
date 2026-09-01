package newapi

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// FMGoBaseURL is the canonical FMGo (feimiao) API root from the vendor guide.
const FMGoBaseURL = "https://api.fmgo.top"

// FMGo chat-completions video dialect (feimiao-v2 / feimiao-v2-fast).
const (
	FMGoChatCompletionsPath = "/v1/chat/completions"
	FMGoTaskPathPrefix      = "/v1/tasks"
)

// Official Seedance 2.0 client ids TokenKey exposes. Runtime rewrite on the
// FMGo video adaptor turns these into feimiao-v2[-fast]-{res}-{dur}s SKUs.
const (
	FMGoSeedanceClientID     = "doubao-seedance-2-0-260128"
	FMGoSeedanceFastClientID = "doubao-seedance-2-0-fast-260128"
)

const (
	FMGoDefaultResolution = "720p"
	FMGoDefaultDuration   = 15
)

// FMGo capability set is pinned in this adaptor. Do not read supplier sources
// or account Extra to derive it.
var (
	fmgoResolutions = map[string]struct{}{
		"480p": {},
		"720p": {},
	}
	fmgoDurations = map[int]struct{}{
		6: {}, 8: {}, 10: {}, 12: {}, 15: {},
	}
	fmgoAspectRatios = map[string]struct{}{
		"16:9": {}, "9:16": {}, "1:1": {}, "2:3": {}, "3:2": {},
	}
)

// IsFMGoBaseURL reports whether channelType/base resolve to FMGo video.
// Scoped to ChannelTypeDoubaoVideo (54), same pattern as IsXRTokenBaseURL.
func IsFMGoBaseURL(channelType int, base string) bool {
	if channelType != newapiconstant.ChannelTypeDoubaoVideo {
		return false
	}
	return NormalizeFMGoBaseURL(base) == FMGoBaseURL
}

// NormalizeFMGoBaseURL collapses accepted FMGo spellings to the canonical root.
// Accepts api.fmgo.top, www.fmgo.top, and fmgo.top, with or without a trailing /v1.
// Non-FMGo hosts pass through unchanged.
func NormalizeFMGoBaseURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return base
	}
	trimmed := strings.TrimSuffix(base, "/v1")
	trimmed = strings.TrimRight(trimmed, "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		if isFMGoHost(trimmed) {
			return FMGoBaseURL
		}
		return base
	}
	if isFMGoHost(parsed.Host) {
		return FMGoBaseURL
	}
	return base
}

func isFMGoHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimSuffix(host, "/")
	return host == "api.fmgo.top" || host == "www.fmgo.top" || host == "fmgo.top"
}

// NormalizeFMGoAspectRatio returns a vendor-legal ratio, or 16:9 when absent/unknown.
func NormalizeFMGoAspectRatio(raw string) string {
	raw = strings.TrimSpace(raw)
	if _, ok := fmgoAspectRatios[raw]; ok {
		return raw
	}
	return "16:9"
}

// IsFMGoSeedanceClient reports a TokenKey-facing official Seedance 2.0 id.
func IsFMGoSeedanceClient(model string) bool {
	switch strings.TrimSpace(model) {
	case FMGoSeedanceClientID, FMGoSeedanceFastClientID:
		return true
	default:
		return false
	}
}

// FMGoSeedanceUpstreamSKU maps an official Seedance client + request params
// onto one FMGo inventory SKU. Empty resolution/duration take the set maximum.
// Values outside the pinned set are rejected; nothing is clamped or downgraded.
func FMGoSeedanceUpstreamSKU(client, resolution string, duration int) (string, error) {
	client = strings.TrimSpace(client)
	if client == "" {
		return "", fmt.Errorf("fmgo seedance: empty model")
	}
	if !IsFMGoSeedanceClient(client) {
		return client, nil
	}
	resolution = strings.ToLower(strings.TrimSpace(resolution))
	if resolution == "" {
		resolution = FMGoDefaultResolution
	}
	if _, ok := fmgoResolutions[resolution]; !ok {
		return "", fmt.Errorf("fmgo seedance: unsupported resolution %q", resolution)
	}
	if duration == 0 {
		duration = FMGoDefaultDuration
	}
	if _, ok := fmgoDurations[duration]; !ok {
		return "", fmt.Errorf("fmgo seedance: unsupported duration %d", duration)
	}
	if client == FMGoSeedanceFastClientID {
		return fmt.Sprintf("feimiao-v2-fast-%s-%ds", resolution, duration), nil
	}
	return fmt.Sprintf("feimiao-v2-%s-%ds", resolution, duration), nil
}

// ParseFMGoVideoDuration accepts a JSON number or numeric string. Zero means absent.
func ParseFMGoVideoDuration(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if strings.HasSuffix(strings.ToLower(raw), "s") {
		raw = strings.TrimSuffix(strings.ToLower(raw), "s")
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("fmgo seedance: duration %q is not an integer", raw)
	}
	return value, nil
}
