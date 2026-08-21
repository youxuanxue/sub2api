package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// ForceRefresh 在上游已明确拒绝当前 access_token 时强制刷新（忽略 3min skew）。
// 成功后清掉本地 cache，避免下一跳再命中被拒的 token。
func (p *OpenAITokenProvider) ForceRefresh(ctx context.Context, account *Account) (*Account, error) {
	if p == nil {
		return account, errors.New("openai token provider is nil")
	}
	if account == nil {
		return nil, errors.New("account is nil")
	}
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return account, errors.New("not an openai oauth account")
	}
	if account.IsOpenAIPersonalAccessToken() {
		return account, errors.New("personal access token cannot be force-refreshed")
	}
	if strings.TrimSpace(account.GetOpenAIRefreshToken()) == "" {
		return account, errors.New("refresh_token missing")
	}
	if p.refreshAPI == nil || p.executor == nil {
		return account, errors.New("oauth refresh api is not configured")
	}

	// 先 RefreshNow 再清 cache。并发 401 的输家若先 DeleteAccessToken，
	// 会把赢家刚写入的新 token 抹掉，然后把 LockHeld 当成刷新失败停号。
	result, err := p.refreshAPI.RefreshNow(ctx, account, p.executor)
	if err != nil {
		return account, err
	}
	if result == nil {
		return account, errors.New("empty oauth refresh result")
	}
	if result.LockHeld {
		refreshed, waitErr := p.awaitForceRefreshWinner(ctx, account)
		if waitErr != nil {
			return account, waitErr
		}
		p.dropOpenAIAccessTokenCache(ctx, refreshed)
		return refreshed, nil
	}
	if result.Account != nil {
		account = result.Account
	}
	if strings.TrimSpace(account.GetOpenAIAccessToken()) == "" {
		return account, errors.New("force refresh did not produce access_token")
	}
	p.dropOpenAIAccessTokenCache(ctx, account)
	return account, nil
}

func (p *OpenAITokenProvider) dropOpenAIAccessTokenCache(ctx context.Context, account *Account) {
	if p == nil || p.tokenCache == nil || account == nil {
		return
	}
	if err := p.tokenCache.DeleteAccessToken(ctx, OpenAITokenCacheKey(account)); err != nil {
		slog.Warn("openai_token_force_refresh_cache_delete_failed",
			"account_id", account.ID, "error", err)
	}
}

// awaitForceRefreshWinner 在 RefreshNow 返回 LockHeld 时复用 GetAccessToken
// 的锁等待节奏，但只接受与被拒 token 不同的新 access_token。
// cache 与 DB 共用同一判定：别人正在 RefreshNow，不是本请求刷新失败。
func (p *OpenAITokenProvider) awaitForceRefreshWinner(ctx context.Context, account *Account) (*Account, error) {
	if p == nil || account == nil {
		return account, errors.New("oauth refresh lock held")
	}
	stale := strings.TrimSpace(account.GetOpenAIAccessToken())
	if p.metrics != nil {
		p.metrics.lockContention.Add(1)
		p.metrics.touchNow()
	}
	if refreshed := p.lookupForceRefreshWinner(ctx, account, stale); refreshed != nil {
		return refreshed, nil
	}

	wait := openAILockInitialWait
	for i := 0; i < openAILockMaxAttempts; i++ {
		actualWait := jitterLockWait(wait)
		timer := time.NewTimer(actualWait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return account, ctx.Err()
		case <-timer.C:
		}
		if p.metrics != nil {
			waitMs := actualWait.Milliseconds()
			if waitMs < 0 {
				waitMs = 0
			}
			p.metrics.lockWaitSamples.Add(1)
			p.metrics.lockWaitTotalMs.Add(waitMs)
			p.metrics.touchNow()
		}
		if refreshed := p.lookupForceRefreshWinner(ctx, account, stale); refreshed != nil {
			if p.metrics != nil {
				p.metrics.lockWaitHit.Add(1)
			}
			return refreshed, nil
		}
		if wait < openAILockMaxWait {
			wait *= 2
			if wait > openAILockMaxWait {
				wait = openAILockMaxWait
			}
		}
	}
	if p.metrics != nil {
		p.metrics.lockWaitMiss.Add(1)
	}
	return account, errors.New("oauth refresh lock held")
}

func (p *OpenAITokenProvider) lookupForceRefreshWinner(ctx context.Context, account *Account, stale string) *Account {
	if p == nil || account == nil {
		return nil
	}
	if p.tokenCache != nil {
		token, err := p.tokenCache.GetAccessToken(ctx, OpenAITokenCacheKey(account))
		if err == nil {
			if next := strings.TrimSpace(token); next != "" && next != stale {
				if p.accountRepo != nil {
					if fresh, rerr := p.accountRepo.GetByID(ctx, account.ID); rerr == nil && fresh != nil {
						if dbToken := strings.TrimSpace(fresh.GetOpenAIAccessToken()); dbToken != "" && dbToken != stale {
							return fresh
						}
					}
				}
				updated := *account
				creds := make(map[string]any, len(account.Credentials)+1)
				for key, value := range account.Credentials {
					creds[key] = value
				}
				creds["access_token"] = next
				updated.Credentials = creds
				return &updated
			}
		}
	}
	if p.accountRepo == nil {
		return nil
	}
	fresh, err := p.accountRepo.GetByID(ctx, account.ID)
	if err != nil || fresh == nil {
		return nil
	}
	if next := strings.TrimSpace(fresh.GetOpenAIAccessToken()); next != "" && next != stale {
		return fresh
	}
	return nil
}

// tkIsPermanentOpenAIAuth401 是 OpenAI 永久认证失败的单一判定。
// HandleUpstreamError 与请求内 RefreshNow 必须共用，避免一边刷新一边停号。
func tkIsPermanentOpenAIAuth401(body []byte) bool {
	switch strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(body))) {
	case "token_invalidated", "token_revoked":
		return true
	}
	return gjson.GetBytes(body, "detail").String() == "Unauthorized"
}

// tkIsRecoverableOpenAI401 识别「当前 access_token 被拒、但 refresh_token 仍可能救活」的 401。
// 能力缺失 / 永久吊销必须继续走既有惩罚路径。
func tkIsRecoverableOpenAI401(statusCode int, body []byte) bool {
	if statusCode != http.StatusUnauthorized {
		return false
	}
	if tkIsCapabilityScope401(statusCode, body) {
		return false
	}
	return !tkIsPermanentOpenAIAuth401(body)
}
