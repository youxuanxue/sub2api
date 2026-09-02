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

// FMGo chat-completions video dialect (legacy feimiao-v2 / feimiao-v2-fast).
// Current default Seedance inventory (v2.5 / 431 / mini) uses /v1/videos.
const (
	FMGoChatCompletionsPath = "/v1/chat/completions"
	FMGoTaskPathPrefix      = "/v1/tasks"
	FMGoVideosPath          = "/v1/videos"
)

// Official Seedance client ids TokenKey exposes. Runtime rewrite on the
// FMGo video adaptor turns these into catalog families that the live default
// group actually serves: 431 / 431-fast / v2.5 (FMGo aliases seedance-2.0*).
const (
	FMGoSeedanceClientID     = "doubao-seedance-2-0-260128"
	FMGoSeedanceFastClientID = "doubao-seedance-2-0-fast-260128"
	FMGoSeedance25ClientID   = "doubao-seedance-2-5-260628"
)

const (
	FMGoDefaultResolution = "720p"
	FMGoDefaultDuration   = 15
	FMGoFamilyV25         = "v2.5"
	FMGoFamily431         = "v2-431"
	FMGoFamily431Fast     = "v2-431-fast"
	FMGoFamilyMini        = "v2-mini"
	FMGoFamilyV2          = "v2"
	FMGoFamilyV2Fast      = "v2-fast"
)

// FMGo capability set is pinned in this adaptor. Do not read supplier sources
// or account Extra to derive it.
var (
	fmgoResolutions = map[string]struct{}{
		"480p": {},
		"720p": {},
	}
	fmgoChatDurations = map[int]struct{}{
		6: {}, 8: {}, 10: {}, 12: {}, 15: {},
	}
	fmgo431Durations = map[int]struct{}{
		10: {}, 15: {},
	}
	fmgoAspectRatios = map[string]struct{}{
		"16:9": {}, "9:16": {}, "1:1": {}, "2:3": {}, "3:2": {},
	}
	fmgoV25Combos = map[string]map[int]struct{}{
		"480p": {5: {}, 10: {}, 15: {}, 30: {}},
		"720p": {10: {}, 15: {}, 30: {}},
	}
	fmgoMiniCombos = map[string]int{
		"720p": 10,
		"480p": 15,
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

// IsFMGoSeedanceClient reports a TokenKey-facing official Seedance id
// that the FMGo adaptor rewrites onto live inventory.
func IsFMGoSeedanceClient(model string) bool {
	switch strings.TrimSpace(model) {
	case FMGoSeedanceClientID, FMGoSeedanceFastClientID, FMGoSeedance25ClientID:
		return true
	default:
		return false
	}
}

// FMGoModelFamily classifies a client or upstream id onto a pinned FMGo family.
// Official Seedance clients map to 431 / 431-fast / v2.5 (live default-group inventory).
func FMGoModelFamily(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case model == "":
		return ""
	case model == FMGoSeedance25ClientID || strings.HasPrefix(model, "feimiao-v2.5"):
		return FMGoFamilyV25
	case model == FMGoSeedanceFastClientID || strings.HasPrefix(model, "feimiao-v2-431-fast"):
		return FMGoFamily431Fast
	case model == FMGoSeedanceClientID || strings.HasPrefix(model, "feimiao-v2-431"):
		return FMGoFamily431
	case strings.HasPrefix(model, "feimiao-v2-mini"):
		return FMGoFamilyMini
	case strings.HasPrefix(model, "feimiao-v2-fast-"):
		return FMGoFamilyV2Fast
	case strings.HasPrefix(model, "feimiao-v2-"):
		return FMGoFamilyV2
	default:
		return ""
	}
}

// FMGoUsesVideosDialect is the official /v1/videos families plus official Seedance clients.
func FMGoUsesVideosDialect(model string) bool {
	switch FMGoModelFamily(model) {
	case FMGoFamilyV25, FMGoFamily431, FMGoFamily431Fast, FMGoFamilyMini:
		return true
	default:
		return false
	}
}

// FMGoSubmitPath is the vendor create path for this model. Seedance clients
// follow the videos dialect of the live default group.
func FMGoSubmitPath(model string) string {
	if FMGoUsesVideosDialect(model) {
		return FMGoVideosPath
	}
	return FMGoChatCompletionsPath
}

// FMGoFetchPath is the vendor poll path for a previously created task.
// Empty model defaults to /v1/videos (live Seedance inventory).
func FMGoFetchPath(model string) string {
	switch FMGoModelFamily(model) {
	case FMGoFamilyV2, FMGoFamilyV2Fast:
		return FMGoTaskPathPrefix
	default:
		return FMGoVideosPath
	}
}

// IsFMGoVideoInventoryID reports catalog ids this adaptor can actually speak:
// v2.5 / 431 / mini / legacy v2, plus official Seedance clients.
// Other video-looking rows (veo/sora/grok-video/omni) stay out so ch54
// candidate probes do not hit them with the Seedance or legacy-v2 dialect.
func IsFMGoVideoInventoryID(model string) bool {
	return FMGoModelFamily(model) != ""
}

// FMGoSeedanceUpstreamSKU maps an official Seedance client + request params
// onto one live-group inventory SKU. Empty resolution/duration take the
// family default. Values outside the pinned family set are rejected.
func FMGoSeedanceUpstreamSKU(client, resolution string, duration int) (string, error) {
	client = strings.TrimSpace(client)
	if client == "" {
		return "", fmt.Errorf("fmgo seedance: empty model")
	}
	family := FMGoModelFamily(client)
	if family == "" {
		return client, nil
	}
	if !IsFMGoSeedanceClient(client) {
		return client, nil
	}
	return fmgoFormatFamilySKU(family, resolution, duration)
}

func fmgoFormatFamilySKU(family, resolution string, duration int) (string, error) {
	resolution = strings.ToLower(strings.TrimSpace(resolution))
	if resolution == "" {
		resolution = FMGoDefaultResolution
	}
	if _, ok := fmgoResolutions[resolution]; !ok {
		return "", fmt.Errorf("fmgo seedance: unsupported resolution %q", resolution)
	}
	if duration == 0 {
		duration = FMGoDefaultDuration
		if family == FMGoFamilyMini && resolution == "720p" {
			duration = 10
		}
	}
	switch family {
	case FMGoFamily431, FMGoFamily431Fast:
		if _, ok := fmgo431Durations[duration]; !ok {
			return "", fmt.Errorf("fmgo seedance: unsupported duration %d", duration)
		}
		if family == FMGoFamily431Fast {
			return fmt.Sprintf("feimiao-v2-431-fast-%s-%ds", resolution, duration), nil
		}
		return fmt.Sprintf("feimiao-v2-431-%s-%ds", resolution, duration), nil
	case FMGoFamilyV25:
		allowed, ok := fmgoV25Combos[resolution]
		if !ok {
			return "", fmt.Errorf("fmgo seedance: unsupported resolution %q", resolution)
		}
		if _, ok = allowed[duration]; !ok {
			return "", fmt.Errorf("fmgo seedance: unsupported duration %d", duration)
		}
		return fmt.Sprintf("feimiao-v2.5-%s-%ds", resolution, duration), nil
	case FMGoFamilyMini:
		want, ok := fmgoMiniCombos[resolution]
		if !ok || want != duration {
			return "", fmt.Errorf("fmgo seedance: unsupported mini combo %s/%ds", resolution, duration)
		}
		return fmt.Sprintf("feimiao-v2-mini-%s-%ds", resolution, duration), nil
	case FMGoFamilyV2, FMGoFamilyV2Fast:
		if _, ok := fmgoChatDurations[duration]; !ok {
			return "", fmt.Errorf("fmgo seedance: unsupported duration %d", duration)
		}
		if family == FMGoFamilyV2Fast {
			return fmt.Sprintf("feimiao-v2-fast-%s-%ds", resolution, duration), nil
		}
		return fmt.Sprintf("feimiao-v2-%s-%ds", resolution, duration), nil
	default:
		return "", fmt.Errorf("fmgo seedance: unknown family %q", family)
	}
}

// FMGoClientForUpstreamSKU rewrites a vendor inventory or echo id back to the
// TokenKey-facing official Seedance client. Unknown ids pass through.
func FMGoClientForUpstreamSKU(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "seedance-2.0-fast", "seeddance-2.0-min", "seedance-2.0-min":
		return FMGoSeedanceFastClientID
	case "seedance-2.0":
		return FMGoSeedanceClientID
	}
	switch FMGoModelFamily(model) {
	case FMGoFamily431Fast, FMGoFamilyV2Fast, FMGoFamilyMini:
		return FMGoSeedanceFastClientID
	case FMGoFamilyV25:
		return FMGoSeedance25ClientID
	case FMGoFamily431, FMGoFamilyV2:
		return FMGoSeedanceClientID
	default:
		return strings.TrimSpace(model)
	}
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
