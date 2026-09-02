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

// TokenKey: runtime loader for the curated newapi long-tail manifest. The file
// contains current policy only: channel/base-URL scopes, price owner and display
// intent. Operational evidence and account history live outside the manifest.

//go:embed tk_served_models.json
var tkServedModelsManifestRaw []byte

type tkServedModelsManifestFile struct {
	SchemaVersion int                                    `json:"schema_version"`
	Entries       map[string]tkServedModelsManifestEntry `json:"entries"`
}

type tkServedModelsManifestEntry struct {
	ChannelType int                           `json:"channel_type"`
	Scopes      []tkServedModelsManifestScope `json:"scopes"`
	PriceOwner  string                        `json:"price_owner"`
	Display     bool                          `json:"display"`
}

type tkServedModelsManifestScope struct {
	ChannelType int    `json:"channel_type"`
	BaseURL     string `json:"base_url"`
}

const tkServedModelsManifestSchemaVersion = 3

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
		if err := json.Unmarshal(tkServedModelsManifestRaw, &doc); err != nil ||
			doc.SchemaVersion != tkServedModelsManifestSchemaVersion {
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
		for modelID, e := range doc.Entries {
			modelID = strings.TrimSpace(modelID)
			if modelID == "" {
				continue
			}
			out[modelID] = struct{}{}
			if e.Display {
				display[modelID] = struct{}{}
			}
			for _, scope := range manifestEntryScopeKeys(e) {
				if byScope[scope] == nil {
					byScope[scope] = make(map[string]struct{})
				}
				byScope[scope][modelID] = struct{}{}
				if e.Display {
					if displayByScope[scope] == nil {
						displayByScope[scope] = make(map[string]struct{})
					}
					displayByScope[scope][modelID] = struct{}{}
				}
			}
			if e.ChannelType <= 0 {
				continue
			}
			if byChannel[e.ChannelType] == nil {
				byChannel[e.ChannelType] = make(map[string]struct{})
			}
			byChannel[e.ChannelType][modelID] = struct{}{}
			if e.Display {
				if displayByChannel[e.ChannelType] == nil {
					displayByChannel[e.ChannelType] = make(map[string]struct{})
				}
				displayByChannel[e.ChannelType][modelID] = struct{}{}
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

func manifestEntryScopeKeys(e tkServedModelsManifestEntry) []string {
	seen := make(map[string]struct{}, len(e.Scopes))
	out := make([]string, 0, len(e.Scopes))
	for _, scope := range e.Scopes {
		key := manifestScopeKey(scope)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func manifestScopeKey(scope tkServedModelsManifestScope) string {
	if !newapiintegration.IsVolcEngineAgentPlanBaseURL(scope.ChannelType, scope.BaseURL) &&
		!newapiintegration.IsQianfanBaseURL(scope.ChannelType, scope.BaseURL) &&
		!newapiintegration.IsXRTokenBaseURL(scope.ChannelType, scope.BaseURL) {
		return ""
	}
	return normalizeAccountModelMappingOverrideScope(PlatformNewAPI, scope.ChannelType, scope.BaseURL)
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

func newAPIQianfanModelMappingPresetIDs() []string {
	return tkServedModelsManifestPresetIDsForSelector(
		PlatformNewAPI,
		newapiconstant.ChannelTypeBaiduV2,
		newapiintegration.QianfanBaseURL,
	)
}

func newAPIQianfanModelDisplayPresetIDs() []string {
	return tkServedModelsManifestDisplayPresetIDsForSelector(
		PlatformNewAPI,
		newapiconstant.ChannelTypeBaiduV2,
		newapiintegration.QianfanBaseURL,
	)
}

func newAPIXRTokenModelMappingPresetIDs() []string {
	return tkServedModelsManifestPresetIDsForSelector(
		PlatformNewAPI,
		newapiconstant.ChannelTypeDoubaoVideo,
		newapiintegration.XRTokenBaseURL,
	)
}

func newAPIXRTokenModelDisplayPresetIDs() []string {
	return tkServedModelsManifestDisplayPresetIDsForSelector(
		PlatformNewAPI,
		newapiconstant.ChannelTypeDoubaoVideo,
		newapiintegration.XRTokenBaseURL,
	)
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
// Moonshot/VolcEngine Ark / Baidu Qianfan wenxin), as opposed to the four native
// platforms + grok which carry their own servable allowlists.
func isNewAPILongTailCatalogVendor(vendor string) bool {
	switch vendor {
	case "newapi", "volcengine", "deepseek", "dashscope", "alibaba", "zhipu", "bigmodel", "zai", "moonshot", "kimi", "wenxin", "qianfan", "baidu":
		return true
	default:
		return false
	}
}
