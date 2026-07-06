package service

import (
	_ "embed"
	"encoding/json"
	"sort"
	"sync"
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
	ModelID     string `json:"model_id"`
	ChannelType int    `json:"channel_type"`
	Display     bool   `json:"display"`
}

var (
	tkServedModelsManifestOnce                    sync.Once
	tkServedModelsManifestIDs                     map[string]struct{}
	tkServedModelsManifestDisplayIDs              map[string]struct{}
	tkServedModelsManifestIDsByChannelType        map[int][]string
	tkServedModelsManifestDisplayIDsByChannelType map[int][]string
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
			return
		}
		out := make(map[string]struct{}, len(doc.Entries))
		display := make(map[string]struct{}, len(doc.Entries))
		byChannel := make(map[int]map[string]struct{})
		displayByChannel := make(map[int]map[string]struct{})
		for _, e := range doc.Entries {
			if e.ModelID == "" {
				continue
			}
			out[e.ModelID] = struct{}{}
			if e.Display {
				display[e.ModelID] = struct{}{}
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
	})
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
// VolcEngine Ark), as opposed to the four native platforms + grok which carry
// their own servable allowlists.
func isNewAPILongTailCatalogVendor(vendor string) bool {
	switch vendor {
	case "newapi", "volcengine", "deepseek", "dashscope", "alibaba", "zhipu", "bigmodel", "zai":
		return true
	default:
		return false
	}
}
