package voice

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

const wavHelp = "use RIFF/WAVE PCM signed 16-bit, 16000 Hz, mono WAV (for example: ffmpeg -i input -ar 16000 -ac 1 -c:a pcm_s16le output.wav)"

func validateWAV(data []byte, maxDuration time.Duration) error {
	invalid := func(reason string) error { return fmt.Errorf("invalid WAV: %s; %s", reason, wavHelp) }
	if len(data) < 44 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return invalid("missing RIFF/WAVE header")
	}
	if uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return invalid("RIFF length does not match file size")
	}
	foundFormat, foundData := false, false
	var sampleBytes uint64
	for offset := uint64(12); offset < uint64(len(data)); {
		if uint64(len(data))-offset < 8 {
			return invalid("truncated chunk header")
		}
		kind := string(data[offset : offset+4])
		size := uint64(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		start := offset + 8
		end := start + size
		if end+size%2 > uint64(len(data)) {
			return invalid("truncated chunk or padding")
		}
		switch kind {
		case "fmt ":
			if foundFormat || size != 16 {
				return invalid("requires exactly one standard 16-byte PCM format chunk")
			}
			format := data[start:end]
			if binary.LittleEndian.Uint16(format[0:2]) != 1 || binary.LittleEndian.Uint16(format[2:4]) != 1 || binary.LittleEndian.Uint32(format[4:8]) != 16000 || binary.LittleEndian.Uint32(format[8:12]) != 32000 || binary.LittleEndian.Uint16(format[12:14]) != 2 || binary.LittleEndian.Uint16(format[14:16]) != 16 {
				return invalid("unsupported sample format or inconsistent rate/alignment")
			}
			foundFormat = true
		case "data":
			if !foundFormat || foundData || size == 0 || size%2 != 0 {
				return invalid("requires one nonempty, aligned data chunk after format")
			}
			foundData = true
			sampleBytes = size
		}
		offset = end + size%2
	}
	if !foundFormat || !foundData {
		return invalid("missing format or data chunk")
	}
	// At 32,000 bytes/second this calculation cannot overflow for RIFF sizes.
	if time.Duration(sampleBytes)*time.Second/32000 > maxDuration {
		return errors.New("WAV exceeds configured audio duration limit")
	}
	return nil
}
