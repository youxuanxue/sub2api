package qa

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

func extractEncryptedReasoningFromStreamChunks(chunks []RawSSEChunk) []string {
	if len(chunks) == 0 {
		return nil
	}
	var out []string
	seen := make(map[string]struct{})
	for _, chunk := range chunks {
		for _, payload := range sseDataJSONPayloads(chunk.Bytes) {
			appendEncryptedReasoningFromJSON(payload, &out, seen)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sseDataJSONPayloads(chunk []byte) [][]byte {
	var out [][]byte
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		out = append(out, payload)
	}
	return out
}

func appendEncryptedReasoningFromJSON(data []byte, out *[]string, seen map[string]struct{}) {
	if !gjson.ValidBytes(data) {
		return
	}
	if item := gjson.GetBytes(data, "item"); item.Exists() {
		appendEncryptedReasoningItem(item, out, seen)
	}
	if output := gjson.GetBytes(data, "response.output"); output.IsArray() {
		output.ForEach(func(_, item gjson.Result) bool {
			appendEncryptedReasoningItem(item, out, seen)
			return true
		})
		return
	}
	if output := gjson.GetBytes(data, "output"); output.IsArray() {
		output.ForEach(func(_, item gjson.Result) bool {
			appendEncryptedReasoningItem(item, out, seen)
			return true
		})
	}
}

func appendEncryptedReasoningItem(item gjson.Result, out *[]string, seen map[string]struct{}) {
	if item.Get("type").String() != "reasoning" {
		return
	}
	enc := strings.TrimSpace(item.Get("encrypted_content").String())
	if enc == "" {
		return
	}
	raw, err := json.Marshal(map[string]string{
		"item_id":           strings.TrimSpace(item.Get("id").String()),
		"encrypted_content": enc,
	})
	if err != nil {
		return
	}
	block := string(raw)
	if _, ok := seen[block]; ok {
		return
	}
	seen[block] = struct{}{}
	*out = append(*out, block)
}

func mergeUniqueStrings(first, second []string) []string {
	if len(first) == 0 && len(second) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(first)+len(second))
	out := make([]string, 0, len(first)+len(second))
	for _, block := range append(append([]string{}, first...), second...) {
		if block == "" {
			continue
		}
		if _, ok := seen[block]; ok {
			continue
		}
		seen[block] = struct{}{}
		out = append(out, block)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
