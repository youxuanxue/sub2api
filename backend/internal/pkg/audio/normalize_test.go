package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func waveFixture(rate, channels, samples int) []byte {
	data := make([]byte, 44+samples*channels*2)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], uint16(channels))
	binary.LittleEndian.PutUint32(data[24:], uint32(rate))
	binary.LittleEndian.PutUint32(data[28:], uint32(rate*channels*2))
	binary.LittleEndian.PutUint16(data[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], uint32(len(data)-44))
	for i := 44; i < len(data); i += 2 {
		binary.LittleEndian.PutUint16(data[i:], 4096)
	}
	return data
}

func TestNormalizeDefaultTTSMP3(t *testing.T) {
	data, err := os.ReadFile("testdata/tts-default-24k.mp3")
	require.NoError(t, err)
	pcm, err := Normalize(context.Background(), data)
	require.NoError(t, err)
	require.Greater(t, len(pcm), PCMBytesPerSecond*2)
	require.Less(t, len(pcm), PCMBytesPerSecond*5)
	require.NotEqual(t, make([]byte, len(pcm)), pcm)
}

func TestNormalizeWAVResamplingAndLimits(t *testing.T) {
	for _, rate := range []int{8000, 16000, 24000, 44100, 48000} {
		for _, channels := range []int{1, 2} {
			pcm, err := Normalize(context.Background(), waveFixture(rate, channels, rate))
			require.NoError(t, err)
			require.InDelta(t, PCMBytesPerSecond, len(pcm), 2)
			require.InDelta(t, 4096, int16(binary.LittleEndian.Uint16(pcm[320:322])), 2)
		}
	}
	pcm, err := Normalize(context.Background(), waveFixture(16000, 1, 60*16000))
	require.NoError(t, err)
	require.Len(t, pcm, 60*PCMBytesPerSecond)
	_, err = Normalize(context.Background(), waveFixture(16000, 1, 60*16000+1))
	require.ErrorIs(t, err, ErrRecordingTooLong)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Normalize(ctx, waveFixture(16000, 1, 16000))
	require.ErrorIs(t, err, context.Canceled)
}

func TestNormalizeRejectsMalformedContainer(t *testing.T) {
	valid := waveFixture(16000, 1, 160)
	hugeChunk := bytes.Clone(valid)
	binary.LittleEndian.PutUint32(hugeChunk[16:], 0x7fffffff)
	wrongAlign := bytes.Clone(valid)
	binary.LittleEndian.PutUint16(wrongAlign[32:], 0)
	for _, data := range [][]byte{nil, []byte("not an MP3"), valid[:len(valid)-1], hugeChunk, wrongAlign, waveFixture(96000, 1, 160), waveFixture(16000, 3, 160)} {
		_, err := Normalize(context.Background(), data)
		require.ErrorIs(t, err, ErrInvalidRecording)
	}
}

func FuzzNormalizeRecording(f *testing.F) {
	f.Add(waveFixture(16000, 1, 160))
	f.Add([]byte("RIFF"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256*1024 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		pcm, err := Normalize(ctx, data)
		if err == nil {
			require.LessOrEqual(t, len(pcm), 60*PCMBytesPerSecond)
		}
	})
}
