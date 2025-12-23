package ipc

import (
	"context"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mithrel/ginkgo/internal/ipc/pb"
	"github.com/mithrel/ginkgo/internal/ipc/transport"
	"github.com/mithrel/ginkgo/pkg/api"
)

// pbHandler adapts a Message handler to protobuf Request/Response.
type pbHandler struct{ fn func(Message) Response }

func (h pbHandler) ProtoTypes() (proto.Message, proto.Message) {
	return &pb.Request{}, &pb.Response{}
}

func (h pbHandler) Handle(ctx context.Context, req any) (any, error) {
	preq := req.(*pb.Request)
	// Convert pb.Request -> Message
	m := Message{}
	switch x := preq.Cmd.(type) {
	case *pb.Request_NoteAdd:
		m.Name = "note.add"
		m.Title = x.NoteAdd.Title
		m.Body = x.NoteAdd.Body
		m.Tags = append([]string(nil), x.NoteAdd.Tags...)
		m.Namespace = x.NoteAdd.Namespace
	case *pb.Request_NoteEdit:
		m.Name = "note.edit"
		m.ID = x.NoteEdit.Id
		m.IfVersion = x.NoteEdit.IfVersion
		m.Title = x.NoteEdit.Title
		m.Body = x.NoteEdit.Body
		m.Tags = append([]string(nil), x.NoteEdit.Tags...)
		m.Namespace = x.NoteEdit.Namespace
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
	case *pb.Request_TagList:
		m.Name = "tag.list"
		if x.TagList != nil {
			m.Namespace = x.TagList.Namespace
		}
	}

	r := h.fn(m)
	// Convert Response -> pb.Response
	presp := &pb.Response{Ok: r.OK, Msg: r.Msg}
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
	return presp, nil
}

// PBHandler builds a transport.Handler around a Message handler.
func PBHandler(fn func(Message) Response) transport.Handler { return pbHandler{fn: fn} }

// Helpers
const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

func toPbEntry(e api.Entry) pb.Entry {
	return pb.Entry{
		Id:        e.ID,
		Version:   e.Version,
		Title:     e.Title,
		Body:      e.Body,
		Tags:      append([]string(nil), e.Tags...),
		CreatedAt: timestamppb.New(e.CreatedAt),
		UpdatedAt: timestamppb.New(e.UpdatedAt),
		Namespace: e.Namespace,
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
}
