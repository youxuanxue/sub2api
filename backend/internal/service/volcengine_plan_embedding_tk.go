package service

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/tidwall/gjson"
)

func volcEnginePlanMultimodalEmbeddingInput(body []byte) bool {
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.IsObject() {
			return true
		}
	}
	return false
}

// Ark combines multimodal parts into one vector and returns data as an object.
// Preserve its usage split while exposing the OpenAI data-array contract.
func normalizeVolcEnginePlanEmbeddingResponse(body []byte) ([]byte, error) {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("invalid Agent Plan embedding response: %w", err)
	}
	var item struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := json.Unmarshal(response["data"], &item); err != nil || len(item.Embedding) == 0 {
		return nil, fmt.Errorf("agent plan embedding response missing vector")
	}
	for _, value := range item.Embedding {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("agent plan embedding response contains non-finite value")
		}
	}
	data, err := json.Marshal([]any{map[string]any{
		"object": "embedding", "index": 0, "embedding": item.Embedding,
	}})
	if err != nil {
		return nil, err
	}
	response["data"] = data
	response["object"] = json.RawMessage(`"list"`)
	return json.Marshal(response)
}
