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
			m.Namespace = x.NoteList.Namespace
			m.TagsAny = append([]string(nil), x.NoteList.TagsAny...)
			m.TagsAll = append([]string(nil), x.NoteList.TagsAll...)
			if x.NoteList.Since != nil {
				m.Since = x.NoteList.Since.AsTime().UTC().Format(timeRFC3339)
			}
			if x.NoteList.Until != nil {
				m.Until = x.NoteList.Until.AsTime().UTC().Format(timeRFC3339)
			}
		}
	case *pb.Request_NoteSearchFts:
		m.Name = "note.search.fts"
		if x.NoteSearchFts != nil {
			m.Title = x.NoteSearchFts.Query
			if f := x.NoteSearchFts.Filter; f != nil {
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
		}
	case *pb.Request_NoteSearchRegex:
		m.Name = "note.search.regex"
		if x.NoteSearchRegex != nil {
			m.Title = x.NoteSearchRegex.Pattern
			if f := x.NoteSearchRegex.Filter; f != nil {
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
