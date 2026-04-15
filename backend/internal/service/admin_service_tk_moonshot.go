package service

import (
	"context"
	"fmt"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	sub2apinewapi "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

func copyCredentialsAnyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func credentialStringFromMap(creds map[string]any, key string) string {
	if creds == nil {
		return ""
	}
	v, ok := creds[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// applyMoonshotRegionalBaseURLAtSave 在创建/更新 newapi Moonshot 账号时，若 base_url 为官方 cn/ai（或空），
// 则并行探测并写入正确区域根 URL（方案 B）。自定义反代 host 不会被覆盖。
// 热路径 relay 不再做区域回退；见 internal/integration/newapi/moonshot_resolve_save.go。
func applyMoonshotRegionalBaseURLAtSave(ctx context.Context, creds map[string]any, platform string, channelType int) error {
	if platform != PlatformNewAPI || channelType != newapiconstant.ChannelTypeMoonshot {
		return nil
	}
	baseStr := credentialStringFromMap(creds, "base_url")
	if !sub2apinewapi.ShouldResolveMoonshotBaseURLAtSave(baseStr) {
		return nil
	}
	apiKey := credentialStringFromMap(creds, "api_key")
	if apiKey == "" {
		return fmt.Errorf("moonshot: api_key is required for regional base_url resolution")
	}
	resolved, err := sub2apinewapi.ResolveMoonshotRegionalBaseAtSave(ctx, apiKey)
	if err != nil {
		return err
	}
	creds["base_url"] = resolved
	return nil
}
