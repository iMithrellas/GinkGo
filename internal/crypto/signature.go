package crypto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
)

// NewSignerKeypair generates an Ed25519 keypair.
func NewSignerKeypair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// SignerID returns a stable identifier for a public key.
func SignerID(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// SignEvent returns an Ed25519 signature for the provided payload bytes.
func SignEvent(priv ed25519.PrivateKey, payload []byte) ([]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid ed25519 private key length")
	}
	return ed25519.Sign(priv, payload), nil
}

// VerifyEvent checks an Ed25519 signature for the payload bytes.
func VerifyEvent(pub ed25519.PublicKey, payload, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid ed25519 public key length")
	}
	if !ed25519.Verify(pub, payload, sig) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

// SignPayload encodes the event fields into a canonical byte format.
func SignPayload(version byte, unixNano int64, eventType, id, namespaceID, payloadType, origin string, payload []byte) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte(version)
	if err := binary.Write(&b, binary.BigEndian, unixNano); err != nil {
		return nil, err
	}
	writeField(&b, eventType)
	writeField(&b, id)
	writeField(&b, namespaceID)
	writeField(&b, payloadType)
	writeField(&b, origin)
	writeBytes(&b, payload)
	return b.Bytes(), nil
}

func writeField(b *bytes.Buffer, s string) {
	writeBytes(b, []byte(s))
}

func writeBytes(b *bytes.Buffer, v []byte) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(v)))
	b.Write(lenBuf[:])
	if len(v) > 0 {
		b.Write(v)
	}
}
