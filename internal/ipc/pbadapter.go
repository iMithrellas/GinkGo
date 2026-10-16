package ipc

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mithrel/ginkgo/internal/ipc/pb"
	"github.com/mithrel/ginkgo/internal/ipc/transport"
	"github.com/mithrel/ginkgo/pkg/api"
)

// pbHandler adapts a Message handler to protobuf Request/Response.
type pbHandler struct {
	fn func(context.Context, Message) Response
}

func (h pbHandler) ProtoTypes() (proto.Message, proto.Message) {
	return &pb.Request{}, &pb.Response{}
}

func (h pbHandler) Handle(ctx context.Context, req any) (any, error) {
	preq := req.(*pb.Request)
	m := Message{}
	switch x := preq.Cmd.(type) {
	case *pb.Request_VoiceTranscribe:
		if n := x.VoiceTranscribe; n != nil {
			m.Name, m.AudioPath, m.Save, m.Mode = "voice.transcribe", n.AudioPath, n.Save, n.Mode
			m.Namespace, m.Title, m.Tags = n.Namespace, n.Title, append([]string(nil), n.Tags...)
			m.Provider, m.Action, m.NoteID = n.Provider, n.Action, n.NoteId
			m.Model, m.Language = n.Model, n.Language
			if n.Deadline != nil {
				if err := n.Deadline.CheckValid(); err != nil {
					return &pb.Response{Ok: false, Msg: "invalid deadline"}, nil
				}
				m.Deadline = n.Deadline.AsTime()
			}
		}
	case *pb.Request_VoiceControl:
		if n := x.VoiceControl; n != nil {
			m.Name, m.Namespace, m.Save, m.Mode = "voice."+n.Action, n.Namespace, n.Save, n.Mode
			m.Provider, m.Title, m.Tags, m.NoteID = n.Provider, n.Title, append([]string(nil), n.Tags...), n.NoteId
			m.Model, m.Language, m.Action = n.Model, n.Language, n.NoteAction
			if c := n.Capture; c != nil {
				m.Capture = &CaptureOptions{Target: c.Target, SilenceTimeout: time.Duration(c.SilenceTimeout), MaxDuration: time.Duration(c.MaxDuration), StartTimeout: time.Duration(c.StartTimeout), Threshold: c.Threshold}
			}
			if n.Deadline != nil {
				if err := n.Deadline.CheckValid(); err != nil {
					return &pb.Response{Ok: false, Msg: "invalid deadline"}, nil
				}
				m.Deadline = n.Deadline.AsTime()
			}
		}
	case *pb.Request_AiNote:
		if x.AiNote != nil {
			n := x.AiNote
			m.Name, m.Text, m.Namespace, m.Title = "ai.note", n.Text, n.Namespace, n.Title
			m.Action, m.NoteID, m.Provider = n.Action, n.NoteId, n.Provider
			m.Tags = append([]string(nil), n.Tags...)
			if n.Deadline != nil {
				if err := n.Deadline.CheckValid(); err != nil {
					return &pb.Response{Ok: false, Msg: "invalid deadline"}, nil
				}
				m.Deadline = n.Deadline.AsTime()
			}
		}
	case *pb.Request_NoteAdd:
		m.Name = "note.add"
		m.Title = x.NoteAdd.Title
		m.Body = x.NoteAdd.Body
		m.Tags = append([]string(nil), x.NoteAdd.Tags...)
		m.Namespace = x.NoteAdd.Namespace
		m.Provenance = fromPbEntry(&pb.Entry{Provenance: x.NoteAdd.Provenance}).Provenance
	case *pb.Request_NoteEdit:
		m.Name = "note.edit"
		m.ID = x.NoteEdit.Id
		m.IfVersion = x.NoteEdit.IfVersion
		m.Title = x.NoteEdit.Title
		m.Body = x.NoteEdit.Body
		m.Tags = append([]string(nil), x.NoteEdit.Tags...)
		m.Namespace = x.NoteEdit.Namespace
		m.Provenance = fromPbEntry(&pb.Entry{Provenance: x.NoteEdit.Provenance}).Provenance
	case *pb.Request_NoteDelete:
		m.Name = "note.delete"
		m.ID = x.NoteDelete.Id
		m.Namespace = x.NoteDelete.Namespace
	case *pb.Request_NoteShow:
		m.Name = "note.show"
		m.ID = x.NoteShow.Id
		m.Namespace = x.NoteShow.Namespace
	case *pb.Request_NoteList:
		m.Name = "note.list"
		if x.NoteList != nil {
			fillFilter(&m, x.NoteList)
		}
	case *pb.Request_NoteSearchFts:
		m.Name = "note.search.fts"
		if x.NoteSearchFts != nil {
			m.Title = x.NoteSearchFts.Query
			fillFilter(&m, x.NoteSearchFts.Filter)
		}
	case *pb.Request_NoteSearchRegex:
		m.Name = "note.search.regex"
		if x.NoteSearchRegex != nil {
			m.Title = x.NoteSearchRegex.Pattern
			fillFilter(&m, x.NoteSearchRegex.Filter)
		}
	case *pb.Request_SyncRun:
		m.Name = "sync.run"
	case *pb.Request_QueueList:
		m.Name = "sync.queue"
		if x.QueueList != nil {
			m.Limit = int(x.QueueList.Limit)
			m.Remote = x.QueueList.Remote
		}
	case *pb.Request_NamespaceList:
		m.Name = "namespace.list"
	case *pb.Request_NamespaceDelete:
		m.Name = "namespace.delete"
		m.Namespace = x.NamespaceDelete.Namespace
	case *pb.Request_TagList:
		m.Name = "tag.list"
		if x.TagList != nil {
			m.Namespace = x.TagList.Namespace
		}
	}

	r := h.fn(ctx, m)
	presp := &pb.Response{Ok: r.OK, Msg: r.Msg, Action: r.Action, FallbackReason: r.FallbackReason}
	if r.Transcript != nil {
		t := r.Transcript
		presp.Transcript = &pb.Transcript{Text: t.Text, Language: t.Language, Engine: t.Engine, Model: t.Model}
	}
	if s := r.VoiceStatus; s != nil {
		ps := &pb.VoiceStatus{Enabled: s.Enabled, Active: s.Active, Continuous: s.Continuous, State: s.State, Namespace: s.Namespace, SilenceTimeout: int64(s.SilenceTimeout), Segments: s.Segments, LastNoteId: s.LastNoteID, LastError: s.LastError}
		if !s.StartedAt.IsZero() {
			ps.StartedAt = timestamppb.New(s.StartedAt)
		}
		presp.VoiceStatus = ps
	}
	if r.Entry != nil {
		e := toPbEntry(*r.Entry)
		presp.Entry = &e
	}
	if len(r.Entries) > 0 {
		presp.Entries = make([]*pb.Entry, 0, len(r.Entries))
		for _, e := range r.Entries {
			ee := toPbEntry(e)
			presp.Entries = append(presp.Entries, &ee)
		}
	}
	if len(r.Queue) > 0 {
		presp.Queue = make([]*pb.QueueRemote, 0, len(r.Queue))
		for _, qr := range r.Queue {
			pqr := &pb.QueueRemote{Name: qr.Name, Url: qr.URL, Pending: qr.Pending}
			if len(qr.Events) > 0 {
				pqr.Events = make([]*pb.QueueEvent, 0, len(qr.Events))
				for _, ev := range qr.Events {
					pqr.Events = append(pqr.Events, &pb.QueueEvent{Time: timestamppb.New(ev.Time), Type: ev.Type, Id: ev.ID})
				}
			}
			presp.Queue = append(presp.Queue, pqr)
		}
	}
	if len(r.Namespaces) > 0 {
		presp.Namespaces = r.Namespaces
	}
	if len(r.Tags) > 0 {
		presp.Tags = make([]*pb.TagStat, 0, len(r.Tags))
		for _, t := range r.Tags {
			presp.Tags = append(presp.Tags, &pb.TagStat{Tag: t.Tag, Count: int32(t.Count), Description: t.Description})
		}
	}
	if r.Page.Next != "" || r.Page.Prev != "" {
		presp.Page = &pb.Page{Next: r.Page.Next, Prev: r.Page.Prev}
	}
	return presp, nil
}

// PBHandler builds a transport.Handler around a Message handler.
func PBHandler(fn func(Message) Response) transport.Handler {
	return PBHandlerContext(func(_ context.Context, m Message) Response { return fn(m) })
}

// PBHandlerContext builds a transport.Handler that propagates the request context.
func PBHandlerContext(fn func(context.Context, Message) Response) transport.Handler {
	return pbHandler{fn: fn}
}

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

func toPbEntry(e api.Entry) pb.Entry {
	var provenance *pb.Provenance
	if e.Provenance != nil {
		provenance = &pb.Provenance{}
		for _, source := range e.Provenance.Sources {
			provenance.Sources = append(provenance.Sources, &pb.Source{
				Id: source.ID, CapturedAt: timestamppb.New(source.CapturedAt),
				Kind: source.Kind, OriginalText: source.OriginalText,
				Language: source.Language, SttEngine: source.STTEngine, SttModel: source.STTModel,
			})
		}
	}
	return pb.Entry{
		Id: e.ID, Version: e.Version, Title: e.Title, Body: e.Body,
		Tags: append([]string(nil), e.Tags...), CreatedAt: timestamppb.New(e.CreatedAt),
		UpdatedAt: timestamppb.New(e.UpdatedAt), Namespace: e.Namespace,
		Provenance: provenance,
	}
}

func fillFilter(m *Message, f *pb.ListFilter) {
	if f == nil {
		return
	}
	m.Namespace = f.Namespace
	m.TagsAny = append([]string(nil), f.TagsAny...)
	m.TagsAll = append([]string(nil), f.TagsAll...)
	if f.Since != nil {
		m.Since = f.Since.AsTime().UTC().Format(timeRFC3339)
	}
	if f.Until != nil {
		m.Until = f.Until.AsTime().UTC().Format(timeRFC3339)
	}
	m.Limit = int(f.Limit)
	m.Cursor = f.Cursor
	m.Reverse = f.Reverse
	m.IncludeBody = f.IncludeBody
}
