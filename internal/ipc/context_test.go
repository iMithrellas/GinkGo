package ipc

import (
	"context"
	"testing"

	"github.com/mithrel/ginkgo/internal/ipc/pb"
)

func TestPBHandlerContextUsesSuppliedContext(t *testing.T) {
	type key struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "request"))
	cancel()
	called := false
	h := PBHandlerContext(func(got context.Context, _ Message) Response {
		called = true
		if got != ctx || got.Value(key{}) != "request" || got.Err() != context.Canceled {
			t.Fatal("handler did not receive supplied context")
		}
		return Response{OK: true}
	})
	response, err := h.Handle(ctx, &pb.Request{})
	if err != nil || !called || !response.(*pb.Response).Ok {
		t.Fatalf("got %v, %v", response, err)
	}
}

func TestPBHandlerCompatibility(t *testing.T) {
	called := false
	h := PBHandler(func(Message) Response { called = true; return Response{OK: true} })
	response, err := h.Handle(context.Background(), &pb.Request{})
	if err != nil || !called || !response.(*pb.Response).Ok {
		t.Fatalf("got %v, %v", response, err)
	}
}
