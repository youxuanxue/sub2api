package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func parseAccountListChannelTypeQuery(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return 0, infraerrors.BadRequest("INVALID_CHANNEL_TYPE_FILTER", "invalid channel_type filter")
	}
	return parsed, nil
}

func buildAccountsListETag(
	items []AccountWithConcurrency,
	total int64,
	page, pageSize int,
	platform, accountType, status, search string,
	channelType int,
	lite bool,
) string {
	payload := struct {
		Total       int64                    `json:"total"`
		Page        int                      `json:"page"`
		PageSize    int                      `json:"page_size"`
		Platform    string                   `json:"platform"`
		AccountType string                   `json:"type"`
		Status      string                   `json:"status"`
		Search      string                   `json:"search"`
		ChannelType int                      `json:"channel_type"`
		Lite        bool                     `json:"lite"`
		Items       []AccountWithConcurrency `json:"items"`
	}{
		Total:       total,
		Page:        page,
		PageSize:    pageSize,
		Platform:    platform,
		AccountType: accountType,
		Status:      status,
		Search:      search,
		ChannelType: channelType,
		Lite:        lite,
		Items:       items,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "\"" + hex.EncodeToString(sum[:]) + "\""
}

func ifNoneMatchMatched(ifNoneMatch, etag string) bool {
	if etag == "" || ifNoneMatch == "" {
		return false
	}
	for _, token := range strings.Split(ifNoneMatch, ",") {
		candidate := strings.TrimSpace(token)
		if candidate == "*" {
			return true
		}
		if candidate == etag {
			return true
		}
		if strings.HasPrefix(candidate, "W/") && strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

// tkBuildAccountListRow projects one service.Account into the list DTO with lite
// shallow mapping, concurrency/RPM/session gauges, and TK mirror-stub EdgeID.
func (h *AccountHandler) tkBuildAccountListRow(
	acc *service.Account,
	lite bool,
	concurrencyCounts map[int64]int,
	schedulerScores map[int64]*AccountSchedulerScore,
	schedulerGroupScores map[int64][]AccountSchedulerGroupScore,
	activeSessions map[int64]int,
	rpmCounts map[int64]int,
) AccountWithConcurrency {
	// In lite mode (list view) use the shallow mapper, which keeps GroupIDs but
	// omits the fully-embedded Groups/AccountGroups objects. Those dominate the
	// list payload (~3.1KB of ~4.5KB per row — the same group definitions are
	// duplicated across every account row), and the list resolves group chips
	// client-side from group_ids + the already-loaded groups list. The full
	// payload (with embedded Groups) is still served for non-lite callers and
	// for single-account detail/edit fetches.
	accountDTO := dto.AccountFromService(acc)
	if lite {
		accountDTO = dto.AccountFromServiceShallow(acc)
	}
	accountDTO = h.enrichAccountResponse(accountDTO)
	item := AccountWithConcurrency{
		Account:            accountDTO,
		CurrentConcurrency: concurrencyCounts[acc.ID],
		SchedulerScore:     schedulerScores[acc.ID],
		SchedulerScores:    schedulerGroupScores[acc.ID],
		// TK: tag anthropic mirror-stub rows with their edge id so the accounts
		// UI can expand them into that edge's accounts inline ("" for non-stubs).
		EdgeID: service.MirrorStubEdgeID(acc),
	}

	// 添加活跃会话数（仅当启用时）
	if activeSessions != nil {
		if count, ok := activeSessions[acc.ID]; ok {
			item.ActiveSessions = &count
		}
	}

	// 添加 RPM 计数（仅当启用时）
	if rpmCounts != nil {
		if rpm, ok := rpmCounts[acc.ID]; ok {
			item.CurrentRPM = &rpm
		}
	}

	return item
}
