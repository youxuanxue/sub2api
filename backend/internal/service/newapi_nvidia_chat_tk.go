package service

import (
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NVIDIA Build accepts max_tokens. With max_completion_tokens the same forced
// tool request can hang before headers even on a direct upstream call. Keep the
// caller's budget, including the Chat converter's max_completion_tokens priority.
func applyNVIDIABuildChatTokenLimit(account *Account, body []byte) []byte {
	if !isNewAPINVIDIABuildAccount(account) {
		return body
	}
	value := gjson.GetBytes(body, "max_completion_tokens")
	limit, err := strconv.ParseInt(value.Raw, 10, 64)
	if !value.Exists() || err != nil || limit <= 0 {
		return body
	}
	next, err := sjson.SetRawBytes(body, "max_tokens", []byte(value.Raw))
	if err != nil {
		return body
	}
	next, err = sjson.DeleteBytes(next, "max_completion_tokens")
	if err != nil {
		return body
	}
	return next
}
