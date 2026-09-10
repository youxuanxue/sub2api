package handler

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"

	"github.com/Wei-Shaw/sub2api/internal/pkg/audio"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func parseAudioTranscription(body []byte, contentType string) (*service.AudioTranscriptionRequest, []byte, error) {
	if len(body) > audio.MaxUploadBytes+64*1024 {
		return nil, nil, fmt.Errorf("audio upload exceeds 25 MiB")
	}
	kind, params, err := mime.ParseMediaType(contentType)
	if err != nil || kind != "multipart/form-data" || params["boundary"] == "" {
		return nil, nil, fmt.Errorf("multipart/form-data with model and file is required")
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	request := &service.AudioTranscriptionRequest{ResponseFormat: "json"}
	seen := map[string]bool{}
	var recording []byte
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("invalid multipart audio upload")
		}
		name := part.FormName()
		if seen[name] {
			return nil, nil, fmt.Errorf("duplicate multipart field %q", name)
		}
		seen[name] = true
		limit := int64(1024)
		if name == "file" {
			limit = audio.MaxUploadBytes
		}
		data, err := io.ReadAll(io.LimitReader(part, limit+1))
		_ = part.Close()
		if err != nil || int64(len(data)) > limit {
			return nil, nil, fmt.Errorf("multipart field too large")
		}
		switch name {
		case "model":
			request.Model = string(data)
		case "response_format":
			request.ResponseFormat = string(data)
		case "file":
			recording = data
		default:
			return nil, nil, fmt.Errorf("unsupported transcription field %q", name)
		}
	}
	if request.Model != service.VolcEnginePlanASRModel {
		return nil, nil, fmt.Errorf("unsupported transcription model")
	}
	if request.ResponseFormat != "json" && request.ResponseFormat != "text" {
		return nil, nil, fmt.Errorf("response_format must be json or text")
	}
	if len(recording) == 0 {
		return nil, nil, fmt.Errorf("file is required")
	}
	return request, recording, nil
}
