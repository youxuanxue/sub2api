package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/audio"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type asrTestConn struct {
	frames        chan []byte
	pcm           []byte
	writes        int
	failWrite     int
	silent        bool
	noFinal       bool
	earlyFinal    bool
	positiveFinal bool
	closed        atomic.Bool
}

func newASRTestConn() *asrTestConn { return &asrTestConn{frames: make(chan []byte, 4)} }
func (c *asrTestConn) WriteJSON(context.Context, any) error {
	return errors.New("unexpected JSON transport")
}
func (c *asrTestConn) Ping(context.Context) error { return nil }
func (c *asrTestConn) Close() error               { c.closed.Store(true); return nil }
func (c *asrTestConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_, frame, err := c.ReadFrame(ctx)
	return frame, err
}
func (c *asrTestConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	select {
	case frame := <-c.frames:
		return coderws.MessageBinary, frame, nil
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}
func (c *asrTestConn) WriteFrame(ctx context.Context, kind coderws.MessageType, frame []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.writes++
	if c.writes == c.failWrite {
		return errors.New("write failed")
	}
	if frame[1]>>4 == 1 {
		c.frames <- encodeVolcEngineASRFrame(9, 1, []byte(`{}`))
		return nil
	}
	c.pcm = append(c.pcm, frame[12:]...)
	if c.earlyFinal || (int32(binary.BigEndian.Uint32(frame[4:8])) < 0 && !c.noFinal) {
		text := "Hello, this is a short audio test."
		if c.silent {
			text = ""
		}
		body, _ := json.Marshal(map[string]any{"audio_info": map[string]any{"duration": len(c.pcm) * 1000 / audio.PCMBytesPerSecond}, "result": map[string]any{"text": text}})
		final := encodeVolcEngineASRFrame(9, -1, body)
		if c.positiveFinal {
			binary.BigEndian.PutUint32(final[4:8], 15)
		}
		c.frames <- final
	}
	return nil
}

type asrTestDialer struct {
	conn    *asrTestConn
	url     string
	headers http.Header
	proxy   string
}

func (d *asrTestDialer) Dial(_ context.Context, url string, headers http.Header, proxy string) (openAIWSClientConn, int, http.Header, error) {
	d.url, d.headers, d.proxy = url, headers, proxy
	return d.conn, 101, http.Header{}, nil
}

func TestVolcEnginePlanASRDefaultTTSRoundTrip(t *testing.T) {
	data, err := os.ReadFile("../pkg/audio/testdata/tts-default-24k.mp3")
	require.NoError(t, err)
	pcm, err := audio.Normalize(context.Background(), data)
	require.NoError(t, err)
	conn := newASRTestConn()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := runVolcEngineASR(ctx, conn, pcm, 0)
	require.NoError(t, err)
	require.Equal(t, pcm, conn.pcm)
	require.Equal(t, "Hello, this is a short audio test.", result.Result.Text)
	require.EqualValues(t, len(pcm)*1000/audio.PCMBytesPerSecond, result.AudioInfo.Duration)
}

func TestVolcEnginePlanASRBinaryWebSocketTransport(t *testing.T) {
	pcm := bytes.Repeat([]byte{1, 0}, 5000)
	received := make(chan []byte, 1)
	serverErrors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := func() error {
			conn, err := coderws.Accept(w, r, nil)
			if err != nil {
				return err
			}
			defer func() { _ = conn.CloseNow() }()
			ctx := r.Context()
			kind, frame, err := conn.Read(ctx)
			if err != nil {
				return err
			}
			if kind != coderws.MessageBinary || len(frame) < 12 || frame[1] != 0x11 || !json.Valid(frame[12:]) {
				return fmt.Errorf("invalid initial request")
			}
			if err := conn.Write(ctx, coderws.MessageBinary, encodeVolcEngineASRFrame(9, 1, []byte(`{}`))); err != nil {
				return err
			}
			var recording []byte
			for {
				kind, frame, err = conn.Read(ctx)
				if err != nil {
					return err
				}
				if kind != coderws.MessageBinary || len(frame) < 12 || frame[1]>>4 != 2 || int(binary.BigEndian.Uint32(frame[8:12])) != len(frame)-12 {
					return fmt.Errorf("invalid audio frame")
				}
				recording = append(recording, frame[12:]...)
				if int32(binary.BigEndian.Uint32(frame[4:8])) < 0 {
					received <- recording
					body := fmt.Sprintf(`{"audio_info":{"duration":%d},"result":{"text":"loopback"}}`, len(recording)*1000/audio.PCMBytesPerSecond)
					final := encodeVolcEngineASRFrame(9, -1, []byte(body))
					binary.BigEndian.PutUint32(final[4:8], 15)
					return conn.Write(ctx, coderws.MessageBinary, final)
				}
			}
		}()
		serverErrors <- err
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()
	result, err := runVolcEngineASR(ctx, &coderOpenAIWSClientConn{conn: conn}, pcm, 0)
	require.NoError(t, err)
	require.NoError(t, <-serverErrors)
	require.Equal(t, pcm, <-received)
	require.Equal(t, "loopback", result.Result.Text)
}

func TestVolcEnginePlanASRFailureAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*asrTestConn)
	}{
		{"write_failure", func(c *asrTestConn) { c.failWrite = 3 }},
		{"missing_terminal", func(c *asrTestConn) { c.noFinal = true }},
		{"premature_terminal", func(c *asrTestConn) { c.earlyFinal = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := newASRTestConn()
			test.setup(conn)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			result, err := runVolcEngineASR(ctx, conn, make([]byte, audio.PCMBytesPerSecond), time.Millisecond)
			require.Error(t, err)
			require.Nil(t, result)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover), "consumed audio must not be replayed")
		})
	}
}

func TestVolcEnginePlanASRForwardSilentInputAndBilling(t *testing.T) {
	conn := newASRTestConn()
	conn.silent = true
	dialer := &asrTestDialer{conn: conn}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, openaiWSPassthroughDialer: dialer}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	account := volcEnginePlanTestAccount()
	result, err := svc.ForwardNativeAudioTranscription(ctx, c, account, &AudioTranscriptionRequest{Model: VolcEnginePlanASRModel, ResponseFormat: "json", PCM: make([]byte, 3200)})
	require.NoError(t, err)
	require.JSONEq(t, `{"text":""}`, recorder.Body.String())
	require.True(t, conn.closed.Load())
	require.Equal(t, volcEnginePlanASRURL, dialer.url)
	require.Equal(t, "volc.seedasr.sauc.duration", dialer.headers.Get("X-Api-Resource-Id"))
	require.Equal(t, "test-plan-key", dialer.headers.Get("X-Api-Key"))
	require.Empty(t, dialer.headers.Get("Authorization"))
	require.InDelta(t, 100.0/3_600_000, result.AudioUsage.DurationOrUnits, 1e-12)
	billing := NewBillingService(nil, nil)
	price := billing.TkRegistrySTTPricePerHour(VolcEnginePlanASRModel)
	require.InDelta(t, 1.0/6.7*1.06, price, 1e-12)
	cost := billing.CalculateAudioCostForModel(VolcEnginePlanASRModel, "stt", 2998.0/3_600_000, nil, 2)
	require.InDelta(t, price*2998.0/3_600_000*2, cost.ActualCost, 1e-12)
	zero := 0.0
	require.Zero(t, billing.CalculateAudioCostForModel(VolcEnginePlanASRModel, "stt", 1, &audioPriceConfig{STTPerHour: &zero}, 1).ActualCost)
}

func TestVolcEnginePlanASRFrameValidation(t *testing.T) {
	valid := encodeVolcEngineASRFrame(9, -1, []byte(`{"audio_info":{"duration":100},"result":{"text":"ok"}}`))
	result, terminal, err := decodeVolcEngineASRFrame(valid)
	require.NoError(t, err)
	require.True(t, terminal)
	require.Equal(t, "ok", result.Result.Text)
	badSeq := bytes.Clone(valid)
	binary.BigEndian.PutUint32(badSeq[4:8], 0)
	negativeNonTerminal := bytes.Clone(valid)
	negativeNonTerminal[1] = 0x91
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, err = writer.Write(bytes.Repeat([]byte(" "), volcEngineASRFrameLimit+1))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	bomb := encodeVolcEngineASRFrame(9, -1, compressed.Bytes())
	bomb[2] = 0x11
	for _, frame := range [][]byte{nil, valid[:7], valid[:len(valid)-1], badSeq, negativeNonTerminal, bomb, encodeVolcEngineASRFrame(9, -1, []byte("bad JSON"))} {
		_, _, err := decodeVolcEngineASRFrame(frame)
		require.Error(t, err)
	}
}

func TestVolcEnginePlanASRPositiveTerminalSequence(t *testing.T) {
	// Observed upstream header on 2026-09-10: 11 93 10 00, sequence +15.
	// Build the response independently of the client audio-frame encoder.
	body := []byte(`{"audio_info":{"duration":2664},"result":{"text":"Hello, this is a short audio test."}}`)
	frame := []byte{0x11, 0x93, 0x10, 0, 0, 0, 0, 15}
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(body)))
	frame = append(frame, body...)
	result, terminal, err := decodeVolcEngineASRFrame(frame)
	require.NoError(t, err)
	require.True(t, terminal)
	require.EqualValues(t, 2664, result.AudioInfo.Duration)
	require.Equal(t, "Hello, this is a short audio test.", result.Result.Text)
}

func TestVolcEnginePlanASRPositiveTerminalForwardAndBilling(t *testing.T) {
	conn := newASRTestConn()
	conn.positiveFinal = true
	svc := &OpenAIGatewayService{cfg: &config.Config{}, openaiWSPassthroughDialer: &asrTestDialer{conn: conn}}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pcm := make([]byte, 6400)
	result, err := svc.ForwardNativeAudioTranscription(ctx, c, volcEnginePlanTestAccount(), &AudioTranscriptionRequest{
		Model: VolcEnginePlanASRModel, ResponseFormat: "json", PCM: pcm,
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"text":"Hello, this is a short audio test."}`, recorder.Body.String())
	require.Equal(t, pcm, conn.pcm)
	require.True(t, conn.closed.Load())
	require.InDelta(t, 200.0/3_600_000, result.AudioUsage.DurationOrUnits, 1e-12)
	cost := NewBillingService(nil, nil).CalculateAudioCostForModel(VolcEnginePlanASRModel, result.AudioUsage.Mode, result.AudioUsage.DurationOrUnits, nil, 1)
	require.InDelta(t, 1.0/6.7*1.06*200/3_600_000, cost.ActualCost, 1e-12)
}
