package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const volcEnginePlanTTSURL = "https://openspeech.bytedance.com/api/v3/plan/tts/unidirectional"
const volcEnginePlanTTSModel = "doubao-seed-tts-2.0"

func SupportsNativeAudioSpeech(account *Account) bool {
	return isNewAPIAliTokenPlanAccount(account) || isNewAPIVolcEngineAgentPlanAccount(account)
}

func ValidateVolcEnginePlanTTSRequest(body []byte) error {
	_, _, err := buildVolcEnginePlanTTSRequest(body)
	return err
}

func (s *OpenAIGatewayService) ForwardNativeAudioSpeech(ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
	if isNewAPIVolcEngineAgentPlanAccount(account) {
		return s.forwardVolcEnginePlanTTS(ctx, c, account, body)
	}
	return s.ForwardAliTokenPlanTTS(ctx, c, account, body)
}

func buildVolcEnginePlanTTSRequest(body []byte) ([]byte, string, error) {
	var request struct {
		Model        string   `json:"model"`
		Input        string   `json:"input"`
		Voice        string   `json:"voice"`
		Format       string   `json:"response_format"`
		SampleRate   int      `json:"sample_rate"`
		Speed        *float64 `json:"speed"`
		Instructions string   `json:"instructions"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, "", fmt.Errorf("invalid speech request")
	}
	if request.Model != volcEnginePlanTTSModel || strings.TrimSpace(request.Input) == "" {
		return nil, "", fmt.Errorf("agent plan TTS requires model %s and nonempty input", volcEnginePlanTTSModel)
	}
	if request.Format == "" {
		request.Format = "mp3"
	}
	format, contentType := request.Format, "audio/mpeg"
	switch format {
	case "mp3":
	case "pcm":
		contentType = "audio/pcm"
	case "opus":
		format, contentType = "ogg_opus", "audio/ogg"
	default:
		return nil, "", fmt.Errorf("agent plan TTS response_format must be mp3, pcm or opus")
	}
	if request.SampleRate == 0 {
		request.SampleRate = 24000
	}
	switch request.SampleRate {
	case 8000, 16000, 22050, 24000, 32000, 44100, 48000:
	default:
		return nil, "", fmt.Errorf("unsupported Agent Plan TTS sample_rate")
	}
	if request.Voice == "" {
		request.Voice = "zh_female_vv_uranus_bigtts"
	}
	audioParams := map[string]any{"format": format, "sample_rate": request.SampleRate}
	if request.Speed != nil {
		if *request.Speed < 0.5 || *request.Speed > 2 {
			return nil, "", fmt.Errorf("agent plan TTS speed must be between 0.5 and 2")
		}
		audioParams["speech_rate"] = int((*request.Speed - 1) * 100)
	}
	params := map[string]any{"text": request.Input, "speaker": request.Voice, "audio_params": audioParams}
	if request.Instructions != "" {
		additions, err := json.Marshal(map[string]any{"context_texts": []string{request.Instructions}})
		if err != nil {
			return nil, "", err
		}
		params["additions"] = string(additions)
	}
	payload, err := json.Marshal(map[string]any{"req_params": params})
	return payload, contentType, err
}

// Buffer until the explicit terminal success so partial audio is never billed
// or returned as a successful synthesis. The caller bounds the response body.
func decodeVolcEnginePlanTTSAudio(body []byte) ([]byte, int64, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var audio bytes.Buffer
	for {
		var event struct {
			Code  *int   `json:"code"`
			Data  string `json:"data"`
			Usage struct {
				TextWords int64 `json:"text_words"`
			} `json:"usage"`
		}
		if err := decoder.Decode(&event); err != nil {
			return nil, 0, fmt.Errorf("incomplete Agent Plan TTS response")
		}
		if event.Code == nil {
			return nil, 0, fmt.Errorf("agent plan TTS response missing status")
		}
		switch *event.Code {
		case 0:
			chunk, err := base64.StdEncoding.DecodeString(event.Data)
			if err != nil {
				return nil, 0, fmt.Errorf("invalid Agent Plan TTS audio encoding")
			}
			_, _ = audio.Write(chunk)
		case 20000000:
			if audio.Len() == 0 || event.Usage.TextWords <= 0 {
				return nil, 0, fmt.Errorf("agent plan TTS response missing audio or billable usage")
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				return nil, 0, fmt.Errorf("unexpected data after Agent Plan TTS completion")
			}
			return audio.Bytes(), event.Usage.TextWords, nil
		default:
			return nil, 0, fmt.Errorf("agent plan TTS synthesis failed (code %d)", *event.Code)
		}
	}
}

func (s *OpenAIGatewayService) forwardVolcEnginePlanTTS(ctx context.Context, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
	if !isNewAPIVolcEngineAgentPlanAccount(account) {
		return nil, fmt.Errorf("agent plan TTS requires a VolcEngine Agent Plan account")
	}
	payload, contentType, err := buildVolcEnginePlanTTSRequest(body)
	if err != nil {
		return nil, err
	}
	token, _, err := s.getRequestCredential(ctx, c, account)
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, volcEnginePlanTTSURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", token)
	req.Header.Set("X-Api-Resource-Id", "seed-tts-2.0")
	req.Header.Set("X-Api-Request-Id", uuid.NewString())
	req.Header.Set("X-Control-Require-Usage-Tokens-Return", "text_words")
	started := time.Now()
	resp, err := s.httpUpstream.Do(req, resolveAccountProxyURL(account), account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, responseBody, volcEnginePlanTTSModel)
		return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: responseBody}
	}
	audio, chars, err := decodeVolcEnginePlanTTSAudio(responseBody)
	if err != nil {
		return nil, err
	}
	c.Data(http.StatusOK, contentType, audio)
	return &OpenAIForwardResult{
		RequestID: StableGrokAudioBillingRequestID(resp.Header.Get("X-Tt-Logid")),
		Model:     volcEnginePlanTTSModel, UpstreamModel: volcEnginePlanTTSModel,
		Duration: time.Since(started), AudioUsage: &AudioUsage{Mode: "tts", DurationOrUnits: float64(chars) / 1_000_000},
	}, nil
}
