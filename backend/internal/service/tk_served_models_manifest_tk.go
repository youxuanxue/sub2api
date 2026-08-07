package service

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// TokenKey: runtime loader for tk_served_models.json — the curated newapi
// long-tail manifest. The drift guard (scripts/checks/catalog-serving-drift.py)
// asserts this file agrees with overlay price + account model_mapping; the
// public /pricing presentation filter and the per-user newapi Group Catalog
// whitelist fallback consume the same embedded source so priced-but-unwired
// models (deepseek-v3-2-251201) and withdrawn SKUs (glm-4-32b upstream 400,
// glm-4-7-251222 VolcEngine duplicate — GLM served via DashScope as glm-4.7)
// cannot mislead the storefront.

//go:embed tk_served_models.json
var tkServedModelsManifestRaw []byte

type tkServedModelsManifestFile struct {
	Entries map[string]tkServedModelsManifestEntry `json:"entries"`
}

type tkServedModelsManifestEntry struct {
	Platform     string                       `json:"platform"`
	ModelID      string                       `json:"model_id"`
	ChannelType  int                          `json:"channel_type"`
	AccountScope *tkServedModelsManifestScope `json:"account_scope"`
	Display      bool                         `json:"display"`
	ServedOn     []string                     `json:"served_on"`
}

type tkServedModelsManifestScope struct {
	Platform    string `json:"platform"`
	ChannelType int    `json:"channel_type"`
	BaseURL     string `json:"base_url"`
}

var (
	tkServedModelsManifestOnce                    sync.Once
	tkServedModelsManifestIDs                     map[string]struct{}
	tkServedModelsManifestDisplayIDs              map[string]struct{}
	tkServedModelsManifestIDsByChannelType        map[int][]string
	tkServedModelsManifestDisplayIDsByChannelType map[int][]string
	tkServedModelsManifestIDsByScope              map[string][]string
	tkServedModelsManifestDisplayIDsByScope       map[string][]string
)

func loadTkServedModelsManifestIDs() map[string]struct{} {
	loadTkServedModelsManifest()
	return tkServedModelsManifestIDs
}

func loadTkServedModelsManifest() {
	tkServedModelsManifestOnce.Do(func() {
		var doc tkServedModelsManifestFile
		if err := json.Unmarshal(tkServedModelsManifestRaw, &doc); err != nil {
			tkServedModelsManifestIDs = map[string]struct{}{}
			tkServedModelsManifestDisplayIDs = map[string]struct{}{}
			tkServedModelsManifestIDsByChannelType = map[int][]string{}
			tkServedModelsManifestDisplayIDsByChannelType = map[int][]string{}
			tkServedModelsManifestIDsByScope = map[string][]string{}
			tkServedModelsManifestDisplayIDsByScope = map[string][]string{}
			return
		}
		out := make(map[string]struct{}, len(doc.Entries))
		display := make(map[string]struct{}, len(doc.Entries))
		byChannel := make(map[int]map[string]struct{})
		displayByChannel := make(map[int]map[string]struct{})
		byScope := make(map[string]map[string]struct{})
		displayByScope := make(map[string]map[string]struct{})
		for _, e := range doc.Entries {
			if e.ModelID == "" {
				continue
			}
			out[e.ModelID] = struct{}{}
			if e.Display {
				display[e.ModelID] = struct{}{}
			}
			if scope := manifestEntryScopeKey(e); scope != "" {
				if byScope[scope] == nil {
					byScope[scope] = make(map[string]struct{})
				}
				byScope[scope][e.ModelID] = struct{}{}
				if e.Display {
					if displayByScope[scope] == nil {
						displayByScope[scope] = make(map[string]struct{})
					}
					displayByScope[scope][e.ModelID] = struct{}{}
				}
				if manifestEntryIsAgentPlanOnly(e) || manifestEntryIsQianfanScopedOnly(e) {
					continue
				}
			}
			if e.ChannelType <= 0 {
				continue
			}
			if byChannel[e.ChannelType] == nil {
				byChannel[e.ChannelType] = make(map[string]struct{})
			}
			byChannel[e.ChannelType][e.ModelID] = struct{}{}
			if e.Display {
				if displayByChannel[e.ChannelType] == nil {
					displayByChannel[e.ChannelType] = make(map[string]struct{})
				}
				displayByChannel[e.ChannelType][e.ModelID] = struct{}{}
			}
		}
		tkServedModelsManifestIDs = out
		tkServedModelsManifestDisplayIDs = display
		tkServedModelsManifestIDsByChannelType = make(map[int][]string, len(byChannel))
		for ct, ids := range byChannel {
			list := make([]string, 0, len(ids))
			for id := range ids {
				list = append(list, id)
			}
			sort.Strings(list)
			tkServedModelsManifestIDsByChannelType[ct] = list
		}
		tkServedModelsManifestDisplayIDsByChannelType = make(map[int][]string, len(displayByChannel))
		for ct, ids := range displayByChannel {
			list := make([]string, 0, len(ids))
			for id := range ids {
				list = append(list, id)
			}
			sort.Strings(list)
			tkServedModelsManifestDisplayIDsByChannelType[ct] = list
		}
		tkServedModelsManifestIDsByScope = make(map[string][]string, len(byScope))
		for scope, ids := range byScope {
			list := make([]string, 0, len(ids))
			for id := range ids {
				list = append(list, id)
			}
			sort.Strings(list)
			tkServedModelsManifestIDsByScope[scope] = list
		}
		tkServedModelsManifestDisplayIDsByScope = make(map[string][]string, len(displayByScope))
		for scope, ids := range displayByScope {
			list := make([]string, 0, len(ids))
			for id := range ids {
				list = append(list, id)
			}
			sort.Strings(list)
			tkServedModelsManifestDisplayIDsByScope[scope] = list
		}
	})
}

func manifestEntryIsAgentPlanOnly(e tkServedModelsManifestEntry) bool {
	return strings.EqualFold(strings.TrimSpace(e.Platform), PlatformNewAPI) &&
		e.AccountScope != nil &&
		e.ChannelType == e.AccountScope.ChannelType &&
		e.ChannelType == newapiconstant.ChannelTypeVolcEngine &&
		newapiintegration.IsVolcEngineAgentPlanBaseURL(e.AccountScope.ChannelType, e.AccountScope.BaseURL)
}

func manifestEntryIsQianfanScopedOnly(e tkServedModelsManifestEntry) bool {
	return strings.EqualFold(strings.TrimSpace(e.Platform), PlatformNewAPI) &&
		e.AccountScope != nil &&
		e.ChannelType == e.AccountScope.ChannelType &&
		e.ChannelType == newapiconstant.ChannelTypeBaiduV2 &&
		newapiintegration.IsQianfanBaseURL(e.AccountScope.ChannelType, e.AccountScope.BaseURL)
}

func manifestEntryScopeKey(e tkServedModelsManifestEntry) string {
	if e.AccountScope == nil ||
		!strings.EqualFold(strings.TrimSpace(e.AccountScope.Platform), PlatformNewAPI) {
		return ""
	}
	if newapiintegration.IsVolcEngineAgentPlanBaseURL(e.AccountScope.ChannelType, e.AccountScope.BaseURL) {
		return normalizeAccountModelMappingOverrideScope(
			e.AccountScope.Platform,
			e.AccountScope.ChannelType,
			e.AccountScope.BaseURL,
		)
	}
	if newapiintegration.IsQianfanBaseURL(e.AccountScope.ChannelType, e.AccountScope.BaseURL) {
		return normalizeAccountModelMappingOverrideScope(
			e.AccountScope.Platform,
			e.AccountScope.ChannelType,
			e.AccountScope.BaseURL,
		)
	}
	return ""
}

func normalizeAccountModelMappingOverrideScope(platform string, channelType int, baseURL string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	baseURL = normalizeAccountModelMappingOverrideBaseURL(baseURL)
	if platform == "" || channelType <= 0 || baseURL == "" {
		return ""
	}
	return platform + ":" + strconv.Itoa(channelType) + ":" + baseURL
}

func tkServedModelsManifestPresetIDsForSelector(platform string, channelType int, baseURL string) []string {
	loadTkServedModelsManifest()
	ids := tkServedModelsManifestIDsByScope[normalizeAccountModelMappingOverrideScope(platform, channelType, baseURL)]
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}

func tkServedModelsManifestDisplayPresetIDsForSelector(platform string, channelType int, baseURL string) []string {
	loadTkServedModelsManifest()
	ids := tkServedModelsManifestDisplayIDsByScope[normalizeAccountModelMappingOverrideScope(platform, channelType, baseURL)]
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}

// qianfanSharedManifestModelIDs lists manifest rows also served on Baidu Qianfan
// account 90 but indexed under other channel_type keys (manifest forbids duplicate model_id).
var qianfanSharedManifestModelIDs = []string{
	"deepseek-v4-pro",
	"deepseek-v4-flash",
	"glm-5.2",
}

func newAPIQianfanModelMappingPresetIDs() []string {
	scoped := tkServedModelsManifestPresetIDsForSelector(
		PlatformNewAPI,
		newapiconstant.ChannelTypeBaiduV2,
		newapiintegration.QianfanBaseURL,
	)
	return mergeSortedManifestModelIDs(scoped, qianfanSharedManifestModelIDs)
}

func newAPIQianfanModelDisplayPresetIDs() []string {
	scoped := tkServedModelsManifestDisplayPresetIDsForSelector(
		PlatformNewAPI,
		newapiconstant.ChannelTypeBaiduV2,
		newapiintegration.QianfanBaseURL,
	)
	return mergeSortedManifestModelIDs(scoped, qianfanSharedManifestModelIDs)
}

func mergeSortedManifestModelIDs(primary, extra []string) []string {
	if len(primary) == 0 && len(extra) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(primary)+len(extra))
	out := make([]string, 0, len(primary)+len(extra))
	for _, id := range primary {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range extra {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// tkServedModelsManifestPresetIDsByChannelType returns empirically verified
// newapi model IDs for a channel_type declared in tk_served_models.json.
// Unknown or unprobed channel types return nil.
func tkServedModelsManifestPresetIDsByChannelType(channelType int) []string {
	loadTkServedModelsManifest()
	if channelType <= 0 {
		return nil
	}
	ids := tkServedModelsManifestIDsByChannelType[channelType]
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}

// tkServedModelsManifestDisplayPresetIDsByChannelType returns the manifest
// subset allowed on public catalog/menu display surfaces. It is narrower than
// tkServedModelsManifestPresetIDsByChannelType, which remains the admin
// model_mapping/provisioning intent list.
func tkServedModelsManifestDisplayPresetIDsByChannelType(channelType int) []string {
	loadTkServedModelsManifest()
	if channelType <= 0 {
		return nil
	}
	ids := tkServedModelsManifestDisplayIDsByChannelType[channelType]
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}

// isTkCuratedNewAPIModelListed reports whether modelID is declared in the
// served-models manifest (newapi long-tail only). Used by the /pricing
// presentation filter and the newapi account whitelist menu fallback.
func isTkCuratedNewAPIModelListed(modelID string) bool {
	if modelID == "" {
		return false
	}
	_, ok := loadTkServedModelsManifestIDs()[modelID]
	return ok
}

// isTkCuratedNewAPIModelDisplayed reports whether a manifest-listed model is
// allowed to appear in public catalog/model-menu surfaces. It is deliberately
// narrower than isTkCuratedNewAPIModelListed: listing means the model is a
// priced/wired runtime candidate; display means the latest SSOT gate says the
// product can safely advertise it.
func isTkCuratedNewAPIModelDisplayed(modelID string) bool {
	if modelID == "" {
		return false
	}
	loadTkServedModelsManifest()
	_, ok := tkServedModelsManifestDisplayIDs[modelID]
	return ok
}

// isTkCuratedNewAPICatalogRowListed is the shared SSOT gate for newapi long-tail
// rows across /pricing display, IsModelPriced membership, and overlay fill.
// Native platforms and unrelated vendors pass through unchanged.
func isTkCuratedNewAPICatalogRowListed(vendor, modelID string) bool {
	if !isNewAPILongTailCatalogVendor(vendor) {
		return true
	}
	return isTkCuratedNewAPIModelListed(modelID)
}

// isTkCuratedNewAPICatalogRowDisplayed is the display-surface sibling of
// isTkCuratedNewAPICatalogRowListed. Hidden manifest rows may still be priced
// and usable by explicitly mapped accounts, but public /pricing must not
// advertise them until the SSOT display gate marks them display=true.
func isTkCuratedNewAPICatalogRowDisplayed(vendor, modelID string) bool {
	if !isNewAPILongTailCatalogVendor(vendor) {
		return true
	}
	return isTkCuratedNewAPIModelDisplayed(modelID)
}

// isNewAPILongTailCatalogVendor reports whether a catalog row's vendor string
// belongs to the fifth-platform newapi curated long-tail (qwen/deepseek/GLM/
// Moonshot/VolcEngine Ark), as opposed to the four native platforms + grok which carry
// their own servable allowlists.
func isNewAPILongTailCatalogVendor(vendor string) bool {
	switch vendor {
	case "newapi", "volcengine", "deepseek", "dashscope", "alibaba", "zhipu", "bigmodel", "zai", "moonshot", "kimi":
		return true
	default:
		return false
	}
}
