package ipc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mithrel/ginkgo/internal/ipc/pb"
)

func TestConversationAdapter(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	h := pbHandler{fn: func(_ context.Context, m Message) Response {
		require.Equal(t, "ai.converse", m.Name)
		require.Equal(t, "session", m.ID)
		require.Equal(t, "work", m.Namespace)
		require.Equal(t, "Read the second one", m.Text)
		require.WithinDuration(t, deadline, m.Deadline, time.Millisecond)
		return Response{OK: true, Msg: "Note body", Action: "read"}
	}}
	result, err := h.Handle(context.Background(), &pb.Request{Cmd: &pb.Request_Conversation{Conversation: &pb.Conversation{Session: "session", Namespace: "work", Text: "Read the second one", Deadline: timestamppb.New(deadline)}}})
	require.NoError(t, err)
	require.Equal(t, "Note body", result.(*pb.Response).Msg)
	require.Equal(t, "read", result.(*pb.Response).Action)
}
