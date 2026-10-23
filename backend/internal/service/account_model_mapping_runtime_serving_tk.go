package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

var (
	accountModelMappingRuntimeServing     atomic.Pointer[accountModelMappingRuntime]
	accountModelMappingRuntimeServingVer  atomic.Uint64
	accountModelMappingRuntimeServingHash atomic.Value // string
	accountModelMappingRuntimeServingMu   sync.Mutex
)

func modelMappingCacheRuntimeVersion() uint64 {
	return xai.RuntimeModelMappingVersion() + accountModelMappingRuntimeServingVersion()
}

func accountModelMappingRuntimeServingVersion() uint64 {
	return accountModelMappingRuntimeServingVer.Load()
}

func antigravityEffectiveDefaultModelMapping() map[string]string {
	return overlayAntigravityDefaultModelMapping(accountModelMappingRuntimeServingPlatform(PlatformAntigravity))
}

// ensureAntigravityDefaultPassthroughs 给已有自定义映射的 Antigravity 账号补上
// 思考档位 wire 与裸名自映射。用户亲手写过的 key 保持不变；这些补齐条目不进入
// capability sync 的显式上游模型清单。
func ensureAntigravityDefaultPassthroughs(result map[string]string) map[string]string {
	if len(result) == 0 || !antigravityMappingWantsDefaultPassthroughs(result) {
		return result
	}
	bases := make(map[string]struct{})
	for wireID, target := range domain.AntigravityThinkingWireFloorEntries() {
		if _, exists := result[wireID]; !exists {
			result[wireID] = target
		}
		base := wireID
		for _, suffix := range []string{"-low", "-medium", "-high", "-tiered"} {
			if strings.HasSuffix(wireID, suffix) {
				base = strings.TrimSuffix(wireID, suffix)
				break
			}
		}
		bases[base] = struct{}{}
	}
	for base := range bases {
		if _, exists := result[base]; !exists {
			result[base] = base
		}
	}
	return result
}

func antigravityMappingWantsDefaultPassthroughs(result map[string]string) bool {
	for key := range result {
		if strings.HasPrefix(key, "gemini-") || strings.HasPrefix(key, "nano-") {
			return true
		}
	}
	return false
}

func overlayAntigravityDefaultModelMapping(runtime map[string]string) map[string]string {
	compiled := domain.DefaultAntigravityModelMapping
	wires := domain.AntigravityThinkingWireFloorEntries()
	out := make(map[string]string, len(compiled)+len(wires)+len(runtime))
	for key, value := range compiled {
		out[key] = value
	}
	for key, value := range wires {
		out[key] = value
	}
	for key, value := range runtime {
		out[key] = value
	}
	return out
}

func accountModelMappingRuntimeServingPlatform(platform string) map[string]string {
	runtime := accountModelMappingRuntimeServing.Load()
	if runtime == nil {
		return nil
	}
	return runtime.platforms[normalizeAccountModelMappingPresetPlatform(platform)]
}

func reloadAccountModelMappingRuntimeServing(ctx context.Context, getter func(context.Context) (string, bool)) error {
	accountModelMappingRuntimeServingMu.Lock()
	defer accountModelMappingRuntimeServingMu.Unlock()

	var blob string
	if getter != nil {
		if value, ok := getter(ctx); ok {
			blob = value
		}
	}
	present := strings.TrimSpace(blob) != ""
	prevHash, _ := accountModelMappingRuntimeServingHash.Load().(string)

	newHash := ""
	if present {
		sum := sha256.Sum256([]byte(blob))
		newHash = hex.EncodeToString(sum[:])
	}
	if newHash == prevHash {
		return nil
	}
	if !present {
		accountModelMappingRuntimeServing.Store(nil)
		accountModelMappingRuntimeServingHash.Store("")
		accountModelMappingRuntimeServingVer.Add(1)
		slog.Info("tk account model mapping runtime serving cleared")
		return nil
	}

	parsed, err := parseAccountModelMappingRuntime(blob)
	if err != nil {
		return fmt.Errorf("account model mapping runtime serving: %w", err)
	}
	accountModelMappingRuntimeServing.Store(parsed)
	accountModelMappingRuntimeServingHash.Store(newHash)
	accountModelMappingRuntimeServingVer.Add(1)
	slog.Info("tk account model mapping runtime serving reloaded",
		"antigravity_keys", len(accountModelMappingRuntimeServingPlatform(PlatformAntigravity)))
	return nil
}

func subscribeAccountModelMappingRuntimeServing(ctx context.Context, bus SettingPubSub, getter func(context.Context) (string, bool)) {
	if bus == nil {
		return
	}
	bus.Subscribe(ctx, func() {
		if err := reloadAccountModelMappingRuntimeServing(ctx, getter); err != nil {
			logger.LegacyPrintf("service.account_model_mapping", "[AccountModelMapping] runtime serving pubsub reload failed: %v", err)
		}
	})
}
