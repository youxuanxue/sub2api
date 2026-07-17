package engine

import "github.com/Wei-Shaw/sub2api/internal/domain"

// IsVideoSupportedForAccount reports whether an account can serve video
// generation, accounting for grok's native (non-task-adaptor) video path.
//
// The canonical video path is the new-api task-adaptor model: a positive
// channel_type whose numeric value maps to a registered new-api TaskAdaptor
// (see IsVideoSupportedChannelType — the single source of truth for the
// newapi/openai bridge video path). grok is the deliberate exception: it is a
// native xAI-OAuth platform whose accounts run channel_type=0 and forward raw
// Bearer to api.x.ai/v1 (no new-api channel). grok serves video through TK's
// own grok-native arm (POST api.x.ai/v1/videos/generations + poll
// GET /v1/videos/{request_id}), NOT through a new-api TaskAdaptor — so it
// qualifies for video WITHOUT a task adaptor and at channel_type=0.
//
// This is intentionally a SEPARATE predicate from IsVideoSupportedChannelType
// (which stays channel_type-derived and unchanged) so the two never drift: a
// future upstream merge touching the task-adaptor registry cannot silently flip
// grok's video eligibility, and a grok account is never mistaken for a newapi
// channel by the channel_type>0 gate. Callers on the video path use THIS
// predicate; the channel_type-derived one stays for the bridge dispatch gates.
func IsVideoSupportedForAccount(platform string, channelType int) bool {
	if platform == domain.PlatformGrok {
		return true
	}
	return IsVideoSupportedChannelType(channelType)
}
