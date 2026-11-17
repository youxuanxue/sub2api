package service

import newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"

// Targets are MiniMax CN wire spellings; serving intent remains in the manifest.
// Floor is evidence-backed (direct upstream chat 2026-11-17 on account 578):
// GET /v1/models and POST /v1/chat/completions on https://api.minimaxi.com
// accepted MiniMax-M3.1-Flash for the CN key.
var miniMaxCNModelTargets = map[string]string{
	"minimax-m3.1-flash": "MiniMax-M3.1-Flash",
}

func isNewAPIMiniMaxCNAccount(account *Account) bool {
	return account != nil && account.Platform == PlatformNewAPI &&
		newapiintegration.IsMiniMaxCNBaseURL(account.ChannelType, account.GetBaseURL())
}

func miniMaxCNModelMapping(account *Account) map[string]string {
	ids := NewAPIModelMappingPresetIDsForAccount(account)
	mapping := make(map[string]string, len(ids))
	for _, id := range ids {
		if target := miniMaxCNModelTargets[id]; target != "" {
			mapping[id] = target
		}
	}
	return mapping
}
