package kiro

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Kiro omits input entirely for an empty argument object. Only reconstruct that
// object when the exact advertised tool schema accepts it. Never repair partial
// JSON, synthesize required arguments, or fetch external schema references.
func toolAcceptsEmptyInput(tools []KiroToolWrapper, name string) bool {
	var schema any
	matches := 0
	for _, tool := range tools {
		if tool.ToolSpecification.Name == name {
			schema = tool.ToolSpecification.InputSchema.JSON
			matches++
		}
	}
	if matches != 1 || schema == nil {
		return false
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return false
	}
	compiler := jsonschema.NewCompiler()
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("external tool schema references are unavailable")
	}
	const resource = "urn:tokenkey:kiro-tool-input"
	if err := compiler.AddResource(resource, bytes.NewReader(encoded)); err != nil {
		return false
	}
	compiled, err := compiler.Compile(resource)
	return err == nil && compiled.Validate(map[string]any{}) == nil
}
