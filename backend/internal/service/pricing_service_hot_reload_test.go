package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

const hotReloadCatalogJSON = `{
	"remote-model": {"litellm_provider": "test", "mode": "chat",
		"input_cost_per_token": 1e-06, "output_cost_per_token": 2e-06}
}`

func hotReloadModelJSON(name string, input, output float64) string {
	return `"` + name + `": {"litellm_provider": "test", "mode": "chat",
		"input_cost_per_token": ` + formatFloat(input) + `, "output_cost_per_token": ` + formatFloat(output) + `}`
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// newHotReloadPricingService 在数据目录里放好目录缓存与 fallback/override 文件并完成首次加载。
// 传空串表示不配置对应文件。
func newHotReloadPricingService(t *testing.T, fallbackJSON, overrideJSON string) *PricingService {
	t.Helper()
	dir := t.TempDir()
	svc := &PricingService{cfg: &config.Config{}}
	svc.cfg.Pricing.DataDir = dir
	require.NoError(t, os.WriteFile(svc.getPricingFilePath(), []byte(hotReloadCatalogJSON), 0644))
	if fallbackJSON != "" {
		svc.cfg.Pricing.FallbackFile = filepath.Join(dir, "fallback.json")
		require.NoError(t, os.WriteFile(svc.cfg.Pricing.FallbackFile, []byte(fallbackJSON), 0644))
	}
	if overrideJSON != "" {
		svc.cfg.Pricing.OverrideFile = filepath.Join(dir, "overrides.json")
		require.NoError(t, os.WriteFile(svc.cfg.Pricing.OverrideFile, []byte(overrideJSON), 0644))
	}
	require.NoError(t, svc.loadPricingData(svc.getPricingFilePath()))
	return svc
}

func TestPricingCustomFilesFingerprint(t *testing.T) {
	svc := &PricingService{cfg: &config.Config{}}
	require.Empty(t, svc.customPricingFilesFingerprint(), "未配置文件返回空串")

	dir := t.TempDir()
	svc.cfg.Pricing.FallbackFile = filepath.Join(dir, "fallback.json")
	missing := svc.customPricingFilesFingerprint()
	require.NotEmpty(t, missing)
	require.Equal(t, missing, svc.customPricingFilesFingerprint(), "同一状态下指纹稳定")

	require.NoError(t, os.WriteFile(svc.cfg.Pricing.FallbackFile, []byte(`{}`), 0644))
	present := svc.customPricingFilesFingerprint()
	require.NotEqual(t, missing, present, "文件出现即视为变化")

	svc.cfg.Pricing.OverrideFile = filepath.Join(dir, "overrides.json")
	require.NoError(t, os.WriteFile(svc.cfg.Pricing.OverrideFile, []byte(`{}`), 0644))
	require.NotEqual(t, present, svc.customPricingFilesFingerprint(), "override 内容参与指纹")
}

func TestPricingHotReload_FallbackChangeRebuildsWithoutTouchingSyncAnchor(t *testing.T) {
	svc := newHotReloadPricingService(t, `{`+hotReloadModelJSON("custom-a", 4e-6, 8e-6)+`}`, "")
	registry := svc.pricingData
	require.Nil(t, svc.pricingData["custom-a"])
	require.Nil(t, svc.pricingData["custom-b"])
	require.NotEmpty(t, svc.customFilesHash)
	anchor, updated := svc.localHash, svc.lastUpdated

	require.NoError(t, os.WriteFile(svc.cfg.Pricing.FallbackFile, []byte(`{`+
		hotReloadModelJSON("custom-a", 5e-6, 8e-6)+`,`+
		hotReloadModelJSON("custom-b", 1e-6, 3e-6)+`}`), 0644))
	svc.reloadIfCustomFilesChanged()

	require.Equal(t, registry, svc.pricingData, "external fallback changes cannot replace registry prices")
	require.Nil(t, svc.pricingData["custom-b"])
	require.Nil(t, svc.pricingData["remote-model"])
	require.Equal(t, anchor, svc.localHash, "热重载不得改动远程同步锚点")
	require.Equal(t, updated, svc.lastUpdated)
	require.Equal(t, svc.customPricingFilesFingerprint(), svc.customFilesHash)
}

func TestPricingHotReload_UnchangedFilesSkipRebuild(t *testing.T) {
	svc := newHotReloadPricingService(t,
		`{`+hotReloadModelJSON("custom-a", 4e-6, 8e-6)+`}`,
		`{"remote-model": {"input_cost_per_token": 7e-06}}`)
	svc.pricingData["sentinel"] = &LiteLLMModelPricing{}

	svc.reloadIfCustomFilesChanged()

	require.Contains(t, svc.pricingData, "sentinel", "指纹未变时不得重建")
}

func TestPricingHotReload_OverrideCannotChangeRegistryOrAddOwners(t *testing.T) {
	svc := newHotReloadPricingService(t, "", `{"gpt-5.4": {"input_cost_per_token": 7e-06}}`)
	registry := svc.pricingData
	require.InDelta(t, 2.5e-6, svc.GetModelPricing("gpt-5.4").InputCostPerToken, 1e-12)

	require.NoError(t, os.WriteFile(svc.cfg.Pricing.OverrideFile, []byte(`{
		"gpt-5.4": {"input_cost_per_token": 9e-06},
		`+hotReloadModelJSON("override-new-model", 5e-6, 1e-5)+`}`), 0644))
	svc.reloadIfCustomFilesChanged()

	require.Equal(t, registry, svc.pricingData)
	require.Nil(t, svc.GetModelPricing("override-new-model"))

	require.NoError(t, os.WriteFile(svc.cfg.Pricing.OverrideFile, []byte(`{}`), 0644))
	svc.reloadIfCustomFilesChanged()

	require.Equal(t, registry, svc.pricingData)
	require.Nil(t, svc.pricingData["override-new-model"])
}

func TestPricingHotReload_InvalidFileKeepsCurrentDataUntilFixed(t *testing.T) {
	svc := newHotReloadPricingService(t, `{`+hotReloadModelJSON("custom-a", 4e-6, 8e-6)+`}`, "")
	before := svc.customFilesHash
	registry := svc.pricingData

	require.NoError(t, os.WriteFile(svc.cfg.Pricing.FallbackFile, []byte(`{"custom-a": {"input_cost_per_token": `), 0644))
	svc.reloadIfCustomFilesChanged()
	require.Equal(t, registry, svc.pricingData)
	require.Equal(t, before, svc.customFilesHash, "指纹不更新，下一轮继续尝试")

	require.NoError(t, os.WriteFile(svc.cfg.Pricing.FallbackFile, []byte(`{`+hotReloadModelJSON("custom-a", 6e-6, 8e-6)+`}`), 0644))
	svc.reloadIfCustomFilesChanged()
	require.Equal(t, registry, svc.pricingData)
	require.NotEqual(t, before, svc.customFilesHash)
}

func TestPricingHotReload_DeletedFilePreservesRegistryAndRecordsFingerprint(t *testing.T) {
	svc := newHotReloadPricingService(t,
		`{`+hotReloadModelJSON("custom-a", 4e-6, 8e-6)+`}`,
		`{"remote-model": {"input_cost_per_token": 7e-06}}`)
	registry := svc.pricingData
	require.Nil(t, svc.pricingData["custom-a"])
	require.Nil(t, svc.pricingData["remote-model"])

	require.NoError(t, os.Remove(svc.cfg.Pricing.OverrideFile))
	svc.reloadIfCustomFilesChanged()
	require.Equal(t, registry, svc.pricingData)

	require.NoError(t, os.Remove(svc.cfg.Pricing.FallbackFile))
	svc.reloadIfCustomFilesChanged()
	require.Equal(t, registry, svc.pricingData)

	svc.pricingData["sentinel"] = &LiteLLMModelPricing{}
	svc.reloadIfCustomFilesChanged()
	require.Contains(t, svc.pricingData, "sentinel", "缺失状态已记录，不得每轮重建")
	delete(registry, "sentinel")

	require.NoError(t, os.WriteFile(svc.cfg.Pricing.FallbackFile, []byte(`{`+hotReloadModelJSON("custom-a", 4e-6, 8e-6)+`}`), 0644))
	svc.reloadIfCustomFilesChanged()
	require.Equal(t, registry, svc.pricingData)
}

type stubPricingRemoteClient struct {
	body      string
	hash      string
	downloads int
}

func (c *stubPricingRemoteClient) FetchPricingJSON(context.Context, string) ([]byte, error) {
	c.downloads++
	return []byte(c.body), nil
}

func (c *stubPricingRemoteClient) FetchHashText(context.Context, string) (string, error) {
	return c.hash, nil
}

func TestPricingSyncRetriesMismatchedCatalogUntilDataCatchesUp(t *testing.T) {
	oldBody := hotReloadCatalogJSON
	newBody := `{` + hotReloadModelJSON("remote-model", 3e-6, 6e-6) + `}`
	oldHash := fmt.Sprintf("%x", sha256.Sum256([]byte(oldBody)))
	newHash := fmt.Sprintf("%x", sha256.Sum256([]byte(newBody)))
	remote := &stubPricingRemoteClient{body: oldBody, hash: newHash}
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		DataDir:   t.TempDir(),
		RemoteURL: "https://example.com/pricing.json",
		HashURL:   "https://example.com/pricing.sha256",
	}}, remote)

	// The hash endpoint has advanced, but the catalog endpoint still serves old prices.
	require.NoError(t, svc.ForceUpdate())
	// A downloaded sensor must never publish an unreviewed runtime owner.
	require.Nil(t, svc.GetModelPricing("remote-model"))
	registry := svc.pricingData
	require.InDelta(t, 2.5e-6, svc.GetModelPricing("gpt-5.4").InputCostPerToken, 1e-12)
	require.Equal(t, oldHash, svc.localHash)
	savedHash, err := os.ReadFile(svc.getHashFilePath())
	require.NoError(t, err)
	require.Equal(t, oldHash+"\n", string(savedHash))

	// An unchanged remote hash must not suppress the later catalog download.
	remote.body = newBody
	require.NoError(t, svc.syncWithRemote())
	require.Equal(t, 2, remote.downloads)
	require.Nil(t, svc.GetModelPricing("remote-model"))
	require.Equal(t, registry, svc.pricingData, "sensor synchronization cannot change the runtime registry")
	require.InDelta(t, 2.5e-6, svc.GetModelPricing("gpt-5.4").InputCostPerToken, 1e-12)
	require.Equal(t, newHash, svc.localHash)
	savedBody, err := os.ReadFile(svc.getPricingFilePath())
	require.NoError(t, err)
	require.Equal(t, newBody, string(savedBody))
	savedHash, err = os.ReadFile(svc.getHashFilePath())
	require.NoError(t, err)
	require.Equal(t, newHash+"\n", string(savedHash))

	// Once both endpoints agree, normal polling skips redundant downloads.
	require.NoError(t, svc.syncWithRemote())
	require.Equal(t, 2, remote.downloads)
}

// 远程下载重建后指纹必须同步到当前文件内容，否则下一轮定时比对会多做一次无意义重载。
func TestPricingHotReload_DownloadRefreshesFingerprint(t *testing.T) {
	svc := newHotReloadPricingService(t, `{`+hotReloadModelJSON("custom-a", 4e-6, 8e-6)+`}`, "")
	svc.cfg.Pricing.RemoteURL = "https://example.com/pricing.json"
	svc.remoteClient = &stubPricingRemoteClient{body: hotReloadCatalogJSON}
	require.NoError(t, os.WriteFile(svc.cfg.Pricing.FallbackFile, []byte(`{`+hotReloadModelJSON("custom-b", 1e-6, 3e-6)+`}`), 0644))

	require.NoError(t, svc.downloadPricingData())

	require.Nil(t, svc.pricingData["custom-b"])
	require.Nil(t, svc.pricingData["custom-a"])
	require.Equal(t, svc.customPricingFilesFingerprint(), svc.customFilesHash)

	svc.pricingData["sentinel"] = &LiteLLMModelPricing{}
	svc.reloadIfCustomFilesChanged()
	require.Contains(t, svc.pricingData, "sentinel", "下载已消化文件变化，不得再次重建")
}

// 只配置了 fallback/override 而没有 remote_url 时调度器也要运行，否则文件改动无人比对。
func TestPricingSchedulerStartsForCustomFilesWithoutRemoteURL(t *testing.T) {
	svc := NewPricingService(&config.Config{Pricing: config.PricingConfig{
		RemoteURL:    "",
		FallbackFile: filepath.Join(t.TempDir(), "fallback.json"),
	}}, nil)

	svc.startUpdateScheduler()
	done := make(chan struct{})
	go func() {
		svc.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("custom file watch must keep the scheduler running")
	case <-time.After(50 * time.Millisecond):
	}

	svc.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler must exit after Stop")
	}
}
