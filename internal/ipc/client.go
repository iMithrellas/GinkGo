package ipc

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mithrel/ginkgo/internal/ipc/pb"
	"github.com/mithrel/ginkgo/internal/ipc/transport"
	"github.com/mithrel/ginkgo/pkg/api"
)

// Request sends a Message to the daemon and waits for a Response using protobuf transport.
func Request(ctx context.Context, path string, m Message) (Response, error) {
	var r Response
	// Build protobuf request
	preq := &pb.Request{}
	switch m.Name {
	case "note.add":
		preq.Cmd = &pb.Request_NoteAdd{NoteAdd: &pb.NoteAdd{Title: m.Title, Body: m.Body, Tags: m.Tags, Namespace: m.Namespace}}
	case "note.edit":
		preq.Cmd = &pb.Request_NoteEdit{NoteEdit: &pb.NoteEdit{Id: m.ID, IfVersion: m.IfVersion, Title: m.Title, Body: m.Body, Tags: m.Tags, Namespace: m.Namespace}}
	case "note.delete":
		preq.Cmd = &pb.Request_NoteDelete{NoteDelete: &pb.NoteDelete{Id: m.ID, Namespace: m.Namespace}}
	case "note.show":
		preq.Cmd = &pb.Request_NoteShow{NoteShow: &pb.NoteShow{Id: m.ID, Namespace: m.Namespace}}
	case "note.list":
		lf := &pb.ListFilter{Namespace: m.Namespace, TagsAny: m.TagsAny, TagsAll: m.TagsAll}
		if ts := parseRFC3339OrEmpty(m.Since); !ts.IsZero() {
			lf.Since = timestamppb.New(ts)
		}
		if ts := parseRFC3339OrEmpty(m.Until); !ts.IsZero() {
			lf.Until = timestamppb.New(ts)
		}
		preq.Cmd = &pb.Request_NoteList{NoteList: lf}
	case "note.search.fts":
		lf := &pb.ListFilter{Namespace: m.Namespace, TagsAny: m.TagsAny, TagsAll: m.TagsAll}
		if ts := parseRFC3339OrEmpty(m.Since); !ts.IsZero() {
			lf.Since = timestamppb.New(ts)
		}
		if ts := parseRFC3339OrEmpty(m.Until); !ts.IsZero() {
			lf.Until = timestamppb.New(ts)
		}
		preq.Cmd = &pb.Request_NoteSearchFts{NoteSearchFts: &pb.SearchFTS{Query: m.Title, Filter: lf}}
	case "note.search.regex":
		lf := &pb.ListFilter{Namespace: m.Namespace, TagsAny: m.TagsAny, TagsAll: m.TagsAll}
		if ts := parseRFC3339OrEmpty(m.Since); !ts.IsZero() {
			lf.Since = timestamppb.New(ts)
		}
		if ts := parseRFC3339OrEmpty(m.Until); !ts.IsZero() {
			lf.Until = timestamppb.New(ts)
		}
		preq.Cmd = &pb.Request_NoteSearchRegex{NoteSearchRegex: &pb.SearchRegex{Pattern: m.Title, Filter: lf}}
	}

	c := transport.NewUnixClient(path)
	// Allocate a response container for unmarshaling
	ctx = transport.WithResp(ctx, &pb.Response{})
	prespAny, err := c.Do(ctx, preq)
	if err != nil {
		return r, err
	}
	presp := prespAny.(*pb.Response)
	r.OK = presp.Ok
	r.Msg = presp.Msg
	if presp.Entry != nil {
		r.Entry = fromPbEntry(presp.Entry)
	}
	if len(presp.Entries) > 0 {
		r.Entries = make([]api.Entry, 0, len(presp.Entries))
		for _, e := range presp.Entries {
			r.Entries = append(r.Entries, *fromPbEntry(e))
		}
	}
	return r, nil
}

func parseRFC3339OrEmpty(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func fromPbEntry(e *pb.Entry) *api.Entry {
	if e == nil {
		return nil
	}
	ae := api.Entry{
		ID:        e.Id,
		Version:   e.Version,
		Title:     e.Title,
		Body:      e.Body,
		Tags:      append([]string(nil), e.Tags...),
		Namespace: e.Namespace,
	}
	if e.CreatedAt != nil {
		ae.CreatedAt = e.CreatedAt.AsTime()
	}
	if e.UpdatedAt != nil {
		ae.UpdatedAt = e.UpdatedAt.AsTime()
	}
	return &ae
}
