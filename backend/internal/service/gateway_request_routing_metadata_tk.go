package service

import "github.com/tidwall/gjson"

// gatewayRequestRoutingMetadata projects only the fields used by stream and
// thinking detection in one top-level walk. Existing selectors and effort
// normalization still own their semantics; preserve raw keys, values and order
// (including duplicate keys), rather than decoding into a last-wins map.
// The full original body remains the canonical request and digest input.
func gatewayRequestRoutingMetadata(body []byte) []byte {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return body
	}
	metadata := make([]byte, 0, 256)
	metadata = append(metadata, '{')
	root.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "stream", "type", "thinking", "reasoning", "reasoning_effort", "model":
			if len(metadata) > 1 {
				metadata = append(metadata, ',')
			}
			metadata = append(metadata, key.Raw...)
			metadata = append(metadata, ':')
			metadata = append(metadata, value.Raw...)
		}
		return true
	})
	return append(metadata, '}')
}
