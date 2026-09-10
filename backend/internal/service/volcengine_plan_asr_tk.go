package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/audio"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const VolcEnginePlanASRModel = "doubao-seed-asr-2.0"
const volcEnginePlanASRURL = "wss://openspeech.bytedance.com/api/v3/plan/sauc/bigmodel_nostream"
const AudioTranscriptionTimeout = 90 * time.Second
const volcEngineASRFrameLimit = 1 << 20
const volcEngineASRChunkBytes = audio.PCMBytesPerSecond / 5
const volcEngineASRChunkInterval = 200 * time.Millisecond

type AudioTranscriptionRequest struct {
	Model          string
	ResponseFormat string
	PCM            []byte
}

func SupportsNativeAudioTranscription(account *Account) bool {
	return isNewAPIVolcEngineAgentPlanAccount(account)
}

type volcEngineASRConn interface {
	ReadFrame(context.Context) (coderws.MessageType, []byte, error)
	WriteFrame(context.Context, coderws.MessageType, []byte) error
	Close() error
}

type volcEngineASRResult struct {
	AudioInfo struct {
		Duration int64 `json:"duration"`
	} `json:"audio_info"`
	Result struct {
		Text string `json:"text"`
	} `json:"result"`
}

func encodeVolcEngineASRFrame(kind byte, sequence int32, payload []byte) []byte {
	flags := byte(1)
	if sequence < 0 {
		flags = 3
	}
	frame := []byte{0x11, kind<<4 | flags, 0x10, 0}
	frame = binary.BigEndian.AppendUint32(frame, uint32(sequence))
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(payload)))
	return append(frame, payload...)
}

func decodeVolcEngineASRFrame(frame []byte) (*volcEngineASRResult, bool, error) {
	invalid := fmt.Errorf("invalid Agent Plan ASR frame")
	if len(frame) < 8 || len(frame) > volcEngineASRFrameLimit || frame[0] != 0x11 || frame[3] != 0 {
		return nil, false, invalid
	}
	kind, flags := frame[1]>>4, frame[1]&15
	if (kind != 9 && kind != 15) || flags > 3 || frame[2]>>4 != 1 || frame[2]&15 > 1 {
		return nil, false, invalid
	}
	payload := frame[4:]
	terminal := flags&2 != 0
	if flags&1 != 0 {
		if len(payload) < 8 {
			return nil, false, invalid
		}
		sequence := int32(binary.BigEndian.Uint32(payload))
		// The server may keep a positive sequence on its terminal response;
		// completion is indicated by flags, independently of sequence sign.
		if sequence == 0 || (!terminal && sequence < 0) {
			return nil, false, invalid
		}
		payload = payload[4:]
	}
	if kind == 15 {
		if len(payload) < 8 {
			return nil, false, invalid
		}
		// Provider error text can contain user data. Keep only the numeric code.
		return nil, false, fmt.Errorf("agent plan ASR failed (code %d)", binary.BigEndian.Uint32(payload))
	}
	if len(payload) < 4 || uint64(binary.BigEndian.Uint32(payload)) != uint64(len(payload)-4) {
		return nil, false, invalid
	}
	payload = payload[4:]
	if frame[2]&15 == 1 {
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, false, invalid
		}
		decoded, err := io.ReadAll(io.LimitReader(reader, volcEngineASRFrameLimit+1))
		_ = reader.Close()
		if err != nil || len(decoded) > volcEngineASRFrameLimit {
			return nil, false, invalid
		}
		payload = decoded
	}
	var result volcEngineASRResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, false, invalid
	}
	return &result, terminal, nil
}

// A recording is replayable only before its first audio frame. Errors after
// sending audio are returned as ordinary errors, never as failover requests.
func runVolcEngineASR(ctx context.Context, conn volcEngineASRConn, pcm []byte, interval time.Duration) (*volcEngineASRResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	request := []byte(`{"user":{"uid":"tokenkey"},"audio":{"format":"pcm","codec":"raw","rate":16000,"bits":16,"channel":1},"request":{"model_name":"bigmodel","enable_itn":true,"enable_punc":true}}`)
	if err := conn.WriteFrame(ctx, coderws.MessageBinary, encodeVolcEngineASRFrame(1, 1, request)); err != nil {
		return nil, err
	}
	read := func() (*volcEngineASRResult, bool, error) {
		kind, frame, err := conn.ReadFrame(ctx)
		if err != nil {
			return nil, false, err
		}
		if kind != coderws.MessageBinary {
			return nil, false, fmt.Errorf("agent plan ASR requires binary frames")
		}
		return decodeVolcEngineASRFrame(frame)
	}
	if _, terminal, err := read(); err != nil {
		return nil, err
	} else if terminal {
		return nil, fmt.Errorf("agent plan ASR ended before audio")
	}
	sent := make(chan error, 1)
	var lastFrameStarted atomic.Bool
	go func() {
		for offset, sequence := 0, int32(2); offset < len(pcm); sequence++ {
			if offset > 0 {
				timer := time.NewTimer(interval)
				select {
				case <-ctx.Done():
					timer.Stop()
					sent <- ctx.Err()
					return
				case <-timer.C:
				}
			}
			end := min(offset+volcEngineASRChunkBytes, len(pcm))
			seq := sequence
			if end == len(pcm) {
				seq = -seq
				lastFrameStarted.Store(true)
			}
			if err := conn.WriteFrame(ctx, coderws.MessageBinary, encodeVolcEngineASRFrame(2, seq, pcm[offset:end])); err != nil {
				sent <- err
				cancel()
				return
			}
			offset = end
		}
		sent <- nil
	}()
	defer func() { cancel(); <-sent }()
	for count := 0; count < 2048; count++ {
		result, terminal, err := read()
		if err != nil {
			return nil, err
		}
		if terminal {
			// Joining the sender also prevents a premature provider terminal event
			// from reporting a successful partial recording.
			if !lastFrameStarted.Load() {
				return nil, fmt.Errorf("agent plan ASR ended before upload completed")
			}
			select {
			case sendErr := <-sent:
				sent <- sendErr
				if sendErr != nil {
					return nil, sendErr
				}
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			expectedMS := int64(len(pcm)) * 1000 / audio.PCMBytesPerSecond
			if result.AudioInfo.Duration <= 0 || result.AudioInfo.Duration > expectedMS+1 || result.AudioInfo.Duration < expectedMS-1 {
				return nil, fmt.Errorf("agent plan ASR returned inconsistent audio duration")
			}
			return result, nil
		}
	}
	return nil, fmt.Errorf("agent plan ASR response limit exceeded")
}

func (s *OpenAIGatewayService) ForwardNativeAudioTranscription(ctx context.Context, c *gin.Context, account *Account, request *AudioTranscriptionRequest) (*OpenAIForwardResult, error) {
	if !SupportsNativeAudioTranscription(account) || request.Model != VolcEnginePlanASRModel || account.GetMappedModel(request.Model) != VolcEnginePlanASRModel || len(request.PCM) == 0 {
		return nil, fmt.Errorf("unsupported Agent Plan transcription request")
	}
	token, _, err := s.getRequestCredential(ctx, c, account)
	if err != nil {
		return nil, err
	}
	headers := http.Header{
		"X-Api-Key":         []string{token},
		"X-Api-Resource-Id": []string{"volc.seedasr.sauc.duration"},
		"X-Api-Connect-Id":  []string{uuid.NewString()},
		"X-Api-Request-Id":  []string{uuid.NewString()},
	}
	started := time.Now()
	dialCtx, cancelDial := context.WithTimeout(ctx, 12*time.Second)
	conn, status, responseHeaders, err := s.getOpenAIWSPassthroughDialer().Dial(dialCtx, volcEnginePlanASRURL, headers, resolveAccountProxyURL(account))
	cancelDial()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if status >= 400 {
			s.handleOpenAIAccountUpstreamError(ctx, account, status, responseHeaders, nil, request.Model)
			return nil, &UpstreamFailoverError{StatusCode: status}
		}
		return nil, fmt.Errorf("agent plan ASR connection failed")
	}
	defer func() { _ = conn.Close() }()
	if native, ok := conn.(*coderOpenAIWSClientConn); ok {
		native.conn.SetReadLimit(volcEngineASRFrameLimit)
	}
	binaryConn, ok := conn.(volcEngineASRConn)
	if !ok {
		return nil, fmt.Errorf("agent plan ASR binary transport unavailable")
	}
	result, err := runVolcEngineASR(ctx, binaryConn, request.PCM, volcEngineASRChunkInterval)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		return nil, err
	}
	if request.ResponseFormat == "text" {
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(result.Result.Text))
	} else {
		c.JSON(http.StatusOK, gin.H{"text": result.Result.Text})
	}
	return &OpenAIForwardResult{
		RequestID: StableGrokAudioBillingRequestID(responseHeaders.Get("X-Tt-Logid")),
		Model:     request.Model, UpstreamModel: VolcEnginePlanASRModel,
		Duration: time.Since(started), AudioUsage: &AudioUsage{Mode: "stt", DurationOrUnits: float64(result.AudioInfo.Duration) / 3_600_000},
	}, nil
}
