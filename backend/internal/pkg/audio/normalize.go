package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"time"

	"github.com/gopxl/beep"
	"github.com/gopxl/beep/mp3"
	"github.com/gopxl/beep/wav"
)

const (
	MaxUploadBytes    = 25 * 1024 * 1024
	MaxDuration       = 60 * time.Second
	SampleRate        = 16000
	PCMBytesPerSecond = SampleRate * 2
)

var ErrInvalidRecording = errors.New("file must contain a valid MP3 or PCM WAV recording (8-48 kHz, mono or stereo)")
var ErrRecordingTooLong = errors.New("recording exceeds 60 seconds")

// Normalize decodes incrementally to bound memory even for highly compressed input.
// The output is PCM16 little-endian, mono, 16 kHz; no uploaded filenames are used.
func Normalize(ctx context.Context, data []byte) (output []byte, decodeErr error) {
	// The pinned MP3 decoder can panic on malformed frame headers. Contain
	// codec panics at this untrusted-input boundary and retain no partial audio.
	defer func() {
		if recover() != nil {
			output, decodeErr = nil, ErrInvalidRecording
		}
	}()
	if len(data) == 0 || len(data) > MaxUploadBytes {
		return nil, ErrInvalidRecording
	}
	var stream beep.StreamSeekCloser
	var format beep.Format
	var err error
	isWAV := bytes.HasPrefix(data, []byte("RIFF"))
	if isWAV {
		if !boundedWAV(data) {
			return nil, ErrInvalidRecording
		}
		stream, format, err = wav.Decode(bytes.NewReader(data))
	} else {
		stream, format, err = mp3.Decode(io.NopCloser(bytes.NewReader(data)))
	}
	if err != nil {
		return nil, ErrInvalidRecording
	}
	defer func() { _ = stream.Close() }()
	if format.SampleRate < 8000 || format.SampleRate > 48000 || format.NumChannels < 1 || format.NumChannels > 2 {
		return nil, ErrInvalidRecording
	}
	var decoded beep.Streamer = stream
	if format.SampleRate != SampleRate {
		decoded = beep.Resample(4, format.SampleRate, SampleRate, stream)
	}
	pcm := make([]byte, 0, PCMBytesPerSecond*3)
	var samples [512][2]float64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, ok := decoded.Stream(samples[:])
		if len(pcm)+n*2 > int(MaxDuration/time.Second)*PCMBytesPerSecond {
			return nil, ErrRecordingTooLong
		}
		for _, sample := range samples[:n] {
			mono := sample[0]
			if format.NumChannels == 2 {
				mono = (sample[0] + sample[1]) / 2
			}
			// beep/wav v1.4.1 divides signed samples by 2^bits-1;
			// restore the standard [-1,1] amplitude before PCM16 quantization.
			if isWAV && format.Precision > 1 {
				bits := format.Precision * 8
				mono *= float64((int64(1)<<bits)-1) / float64(int64(1)<<(bits-1))
			}
			mono = math.Max(-1, math.Min(1, mono))
			value := int16(math.Max(-32768, math.Min(32767, math.Round(mono*32768))))
			pcm = binary.LittleEndian.AppendUint16(pcm, uint16(value))
		}
		if !ok {
			if decoded.Err() != nil || len(pcm) == 0 {
				return nil, ErrInvalidRecording
			}
			return pcm, nil
		}
	}
}

// The decoder allocates RIFF chunks using their declared lengths. Validate the
// container boundaries first so a tiny upload cannot request a huge allocation.
func boundedWAV(data []byte) bool {
	if len(data) < 12 || string(data[8:12]) != "WAVE" || uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return false
	}
	fmtSeen, dataSeen := false, false
	blockAlign := 0
	for pos := 12; pos < len(data); {
		if len(data)-pos < 8 {
			return false
		}
		size := uint64(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		if size > uint64(len(data)-pos-8) {
			return false
		}
		chunk := data[pos+8 : pos+8+int(size)]
		switch string(data[pos : pos+4]) {
		case "fmt ":
			if fmtSeen || dataSeen || len(chunk) < 16 || binary.LittleEndian.Uint16(chunk) != 1 {
				return false
			}
			channels := int(binary.LittleEndian.Uint16(chunk[2:4]))
			bits := int(binary.LittleEndian.Uint16(chunk[14:16]))
			blockAlign = int(binary.LittleEndian.Uint16(chunk[12:14]))
			rate := uint64(binary.LittleEndian.Uint32(chunk[4:8]))
			if channels < 1 || channels > 2 || (bits != 8 && bits != 16 && bits != 24) || blockAlign != channels*bits/8 || rate < 8000 || rate > 48000 || uint64(binary.LittleEndian.Uint32(chunk[8:12])) != rate*uint64(blockAlign) {
				return false
			}
			fmtSeen = true
		case "data":
			if !fmtSeen || dataSeen || len(chunk) == 0 || len(chunk)%blockAlign != 0 {
				return false
			}
			dataSeen = true
		}
		pos += 8 + int(size) + int(size%2)
		if pos > len(data) {
			return false
		}
	}
	return fmtSeen && dataSeen
}
