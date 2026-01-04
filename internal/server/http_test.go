package server

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/spf13/viper"
	"google.golang.org/protobuf/types/known/timestamppb"

	gcrypto "github.com/mithrel/ginkgo/internal/crypto"
	pbmsg "github.com/mithrel/ginkgo/internal/ipc/pb"
)

func TestVerifyRepEventSignature(t *testing.T) {
	pub, priv, err := gcrypto.NewSignerKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	cfg := viper.New()
	cfg.Set("namespaces.test.trusted_signers", []string{base64.StdEncoding.EncodeToString(pub)})
	srv := &Server{cfg: cfg}

	now := time.Now().UTC()
	payload := []byte("hello")
	signBytes, err := gcrypto.SignPayload(1, now.UnixNano(), "upsert", "id1", "test", "plain_v1", "host", payload)
	if err != nil {
		t.Fatalf("sign payload: %v", err)
	}
	sig, err := gcrypto.SignEvent(priv, signBytes)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ev := &pbmsg.RepEvent{
		Time:        timestamppb.New(now),
		Type:        "upsert",
		Id:          "id1",
		NamespaceId: "test",
		PayloadType: "plain_v1",
		Payload:     payload,
		OriginLabel: "host",
		SignerId:    gcrypto.SignerID(pub),
		Sig:         sig,
	}
	if err := srv.verifyRepEventSignature(ev); err != nil {
		t.Fatalf("expected valid signature, got %v", err)
	}

	evMissing := &pbmsg.RepEvent{
		Time:        ev.Time,
		Type:        ev.Type,
		Id:          ev.Id,
		NamespaceId: ev.NamespaceId,
		PayloadType: ev.PayloadType,
		Payload:     ev.Payload,
		OriginLabel: ev.OriginLabel,
		SignerId:    ev.SignerId,
	}
	if err := srv.verifyRepEventSignature(evMissing); err == nil {
		t.Fatalf("expected missing signature error")
	}
}
