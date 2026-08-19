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

// Empty-mapping Antigravity serving overlay.
//
// The compiled DefaultAntigravityModelMapping is the last-known-good allowlist
// for accounts that do not persist credentials.model_mapping. The ops setting
// tk_account_model_mapping_runtime is also a desired floor for apply-accounts;
// until this file existed, settings fan-out never reached GetModelMapping, so
// empty Antigravity accounts (typical edge OAuth rows) could not hot-add a
// model. Overlay semantics keep compiled keys and let runtime keys add or
// override, so a partial platforms.antigravity blob cannot drop live models.
// Non-empty account mappings stay authoritative.

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

func overlayAntigravityDefaultModelMapping(runtime map[string]string) map[string]string {
	compiled := domain.DefaultAntigravityModelMapping
	if len(runtime) == 0 {
		return compiled
	}
	out := make(map[string]string, len(compiled)+len(runtime))
	for key, value := range compiled {
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

func resetAccountModelMappingRuntimeServingForTest() {
	accountModelMappingRuntimeServingMu.Lock()
	defer accountModelMappingRuntimeServingMu.Unlock()
	accountModelMappingRuntimeServing.Store(nil)
	accountModelMappingRuntimeServingVer.Store(0)
	accountModelMappingRuntimeServingHash.Store("")
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
