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
	// 只注入档位 wire（-low/-medium/-high）。这些 ID 上游目录里确实存在，而
	// resolveGeminiThinkingVariantForLevel 需要在 mapping 里查到 base+"-"+level
	// 才能按 thinking 档位派发；它们没有定价行，不会上 /v1/models 或定价菜单。
	//
	// 2026-10-23 删掉了同时注入的「裸名自映射」（result[base] = base）。上游加
	// 它是为了升级兼容性：存量 mapping 早于某次模型发布时，裸名不会凭空消失。
	// 但 TK 走的是对账落盘路线 —— 现网 26 个 Antigravity 账号的 model_mapping
	// 已经把裸名显式写成 "gemini-3.8-flash": "gemini-3.8-flash-high"，上游要解决
	// 的「存量账号缺键」在 TK prod 上并不存在。
	//
	// 而裸名自映射指向的是上游目录里没有的名字（AG 只有档位变体），本身就是
	// 404 的来源，并且与 DefaultAntigravityModelMapping 的「裸名锁 -high」约定
	// 语义相反 —— 两套真相。实测它在现网的净增量只有一条：把已退役的
	// gemini-3.6-flash 复活回客户可见列表。
	//
	// 代价（刻意接受）：以后新模型上线、或从别处导入旧账号时，裸名要靠对账脚本
	// 或 modelops activation 补齐，不再自动兜住。
	for wireID, target := range domain.AntigravityThinkingWireFloorEntries() {
		if _, exists := result[wireID]; !exists {
			result[wireID] = target
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
