package newapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// PlatformNewAPI mirrors service.PlatformNewAPI without importing the service
// package (which would create an import cycle: service -> integration/newapi -> service).
const PlatformNewAPI = "newapi"

// MaybeResolveMoonshotBaseURLForNewAPI performs the save-time Moonshot regional
// probe iff the account is a newapi/Moonshot account whose configured base URL
// targets the official api.moonshot.cn / api.moonshot.ai roots. It returns
// (resolvedBase, didResolve, err):
//
//   - didResolve=true means the caller MUST overwrite credentials["base_url"]
//     with resolvedBase (no trailing slash) so the relay hot path uses the
//     correct region without per-request fallback.
//   - didResolve=false (and err==nil) means the helper intentionally skipped
//     resolution (non-newapi platform, non-Moonshot channel_type, custom
//     reverse-proxy host, or empty api key); the caller should not modify
//     base_url.
//   - err!=nil means probing failed on every official base; the caller should
//     surface this to the operator instead of silently saving the account
//     with a potentially wrong region.
//
// This helper exists because moonshot_resolve_save.go's ResolveMoonshotRegional
// BaseAtSave was previously only wired into the admin "fetch model list" path
// (fetch_upstream_models.go), so newapi/Moonshot accounts created via the
// admin UI silently kept whatever base_url the user typed. See Bug B notes in
// docs/approved/admin-ui-newapi-platform-end-to-end.md.
func MaybeResolveMoonshotBaseURLForNewAPI(ctx context.Context, platform string, channelType int, baseURL, apiKey string) (resolved string, didResolve bool, err error) {
	if platform != PlatformNewAPI {
		return "", false, nil
	}
	if channelType != newapiconstant.ChannelTypeMoonshot {
		return "", false, nil
	}
	if !ShouldResolveMoonshotBaseURLAtSave(baseURL) {
		return "", false, nil
	}
	if strings.TrimSpace(apiKey) == "" {
		// Validation of credential completeness is the caller's responsibility;
		// we just skip cold probing rather than fail the save with a confusing
		// "moonshot regional resolve: api key is empty" error.
		return "", false, nil
	}
	r, err := ResolveMoonshotRegionalBaseAtSave(ctx, apiKey)
	if err != nil {
		return "", false, err
	}
	return r, true, nil
}

// Moonshot 国内站 (api.moonshot.cn) 与国际站 (api.moonshot.ai) 使用不同密钥体系。
//
// 方案 B（本文件）：在「保存账号」时并行探测两个官方 OpenAI 兼容根地址（GET /v1/models），
// 将首个返回 200 的 host 写入 credentials.base_url。之后热路径（relay）只访问该 URL，
// 不在每次请求上做区域回退，从而避免额外 RTT。
//
// 若用户使用自建反代等非官方 host，ShouldResolveMoonshotBaseURLAtSave 返回 false，跳过探测，
// 完全尊重用户填写的 base_url。

var moonshotOfficialProbeBases = []string{
	"https://api.moonshot.cn",
	"https://api.moonshot.ai",
}

// moonshotProbeBasesForTest 非空时替换 moonshotOfficialProbeBases，仅供同包测试注入 httptest URL。
var moonshotProbeBasesForTest []string

// ShouldResolveMoonshotBaseURLAtSave 为 true 时，保存账号前应对 base_url 做并行区域探测并覆盖。
// 官方 cn/ai 根域名（或空，由调用方保证有默认）需要解析；其它 host 视为用户自定义反代，不覆盖。
func ShouldResolveMoonshotBaseURLAtSave(baseURL string) bool {
	s := strings.TrimSpace(baseURL)
	if s == "" {
		return true
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return true
	}
	h := strings.ToLower(u.Hostname())
	return h == "api.moonshot.cn" || h == "api.moonshot.ai"
}

// ResolveMoonshotRegionalBaseAtSave 并行请求两个官方 Moonshot 根上的 GET /v1/models，返回首个鉴权成功的根 URL（无尾部斜杠）。
// 仅应在保存账号或管理员「获取模型列表」等冷路径调用；不要在 relay 热路径调用。
func ResolveMoonshotRegionalBaseAtSave(ctx context.Context, apiKey string) (string, error) {
	key := strings.TrimSpace(apiKey)
	if i := strings.IndexByte(key, '\n'); i >= 0 {
		key = key[:i]
	}
	if key == "" {
		return "", fmt.Errorf("moonshot regional resolve: api key is empty")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	// 子 context：任一探测成功即取消其余请求，减少无谓等待。
	probeCtx, probeCancel := context.WithCancel(ctx)
	defer probeCancel()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var winner string
	bases := moonshotOfficialProbeBases
	if len(moonshotProbeBasesForTest) > 0 {
		bases = moonshotProbeBasesForTest
	}
	errs := make([]error, len(bases))

	for i, rawBase := range bases {
		i, rawBase := i, rawBase
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := moonshotProbeModelsOK(probeCtx, rawBase, key)
			mu.Lock()
			defer mu.Unlock()
			errs[i] = err
			if err != nil {
				return
			}
			if winner == "" {
				winner = rawBase
				probeCancel()
			}
		}()
	}
	wg.Wait()

	if winner != "" {
		return strings.TrimRight(strings.TrimSpace(winner), "/"), nil
	}
	return "", fmt.Errorf("moonshot regional resolve: %v; %v", errs[0], errs[1])
}

func moonshotProbeModelsOK(ctx context.Context, baseRoot, apiKey string) error {
	baseRoot = strings.TrimRight(strings.TrimSpace(baseRoot), "/")
	if baseRoot == "" {
		return fmt.Errorf("empty base")
	}
	u := baseRoot + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("upstream returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}
