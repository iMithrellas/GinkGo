package sherpavoice

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
)

// Hashes bind the audited FP32 chunk16 profile, not filenames.
var frozenKWSHashes = [4]string{
	"540ff509ed89bd22afe04bf7049a54bb1c95c6d8a18742ea9691910cdb5f859e",
	"63a22dd60f40fff082ac3e09afa507f6787da36df76ded2fbe145fa233e22c21",
	"76f7a24ed0c08633af14b2ee377f747af880d3b65eeba2cd3f31f3380fb73e8d",
	"2d3f32311f9b692b964da3c90e830258d3e78e013cb0c992dbfb15cd5a1a71b0",
}

// compiledWakeLabel requires the whole audited path. Labels are not speech.
func compiledWakeLabel(text string) (string, error) {
	if strings.ContainsAny(strings.TrimSpace(text), "\r\n") {
		return "", errors.New("compiled wake must contain exactly one path")
	}
	fields := strings.Fields(text)
	phones := []string{"HH", "EY1", "G", "IH1", "NG", "K", "OW1"}
	if len(fields) < 7 {
		return "", errors.New("unsupported compiled wake path")
	}
	for i, p := range phones {
		if fields[i] != p {
			return "", errors.New("unsupported compiled wake path")
		}
	}
	label := ""
	score, threshold := false, false
	for _, field := range fields[7:] {
		switch {
		case field == ":1" && !score:
			score = true
		case field == "#0.25" && !threshold:
			threshold = true
		case strings.HasPrefix(field, "@") && label == "":
			label = strings.TrimPrefix(field, "@")
			if len(label) == 0 || len(label) > 64 {
				return "", errors.New("invalid compiled label")
			}
			for _, r := range label {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
					return "", errors.New("invalid compiled label")
				}
			}
		default:
			return "", errors.New("unsupported compiled wake options")
		}
	}
	return label, nil
}

func readWakeLabel(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("open compiled wake file")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1025))
	if err != nil || len(b) > 1024 {
		return "", errors.New("invalid compiled wake file size")
	}
	return compiledWakeLabel(string(b))
}

func attestKWS(data []byte, index int) error {
	h := sha256.Sum256(data)
	if index < 0 || index >= len(frozenKWSHashes) || hex.EncodeToString(h[:]) != frozenKWSHashes[index] {
		return errors.New("unsupported KWS artifact: audited FP32 chunk16 profile required")
	}
	return nil
}
