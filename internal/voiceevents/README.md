# Live voice events (NDJSON v1)

Read-only subscriptions; opening this endpoint **never** starts capture, loads a
model, changes a note, or sends an ordinary protobuf request. The daemon optionally
hosts this package with `voice.events.enabled`; [`voice stream`](../../docs/voice-stream.md)
publishes observations (legacy jobs do not). There are no transcript/audio logs and no audio bytes
on the wire.

## Hosting API

```go
hub, err := voiceevents.NewHub(voiceevents.Options{}) // handle err
path, err := voiceevents.SocketPath()               // handle err
server, err := voiceevents.Listen(ctx, path, hub)    // handle err
defer hub.Close()
defer server.Close()

err = hub.UpdateState(voiceevents.State{
    Listening: true, RecordingNote: true, ActiveNoteSessionID: "note-session-1",
})
err = hub.Publish(voiceevents.Event{
    Type: "transcript", UtteranceID: "utterance-1", Revision: 1,
    Text: "revisable text", NoteSessionID: "note-session-1",
})
snapshot := hub.Snapshot()
```

Handle every returned error. `NewHub` generates a cryptographically random epoch;
create one hub per daemon start. `UpdateState(State) error` atomically replaces
the live state and emits `state`. `Publish(Event) error` stamps the envelope and
current state. `Snapshot() Event` returns just current state, not transcript
history. `Hub.Close()` disconnects subscribers and rejects later publications.
`Listen(context.Context, string, *Hub) (*Server, error)` starts the endpoint;
`Server.Close() error` disconnects/joins socket workers and removes its own socket.
`Server.Wait() error` observes/join completion without initiating shutdown.
Unexpected accept errors stop the endpoint and are returned by Wait/Close;
listener shutdown and owned-socket removal errors are also returned. Hosts should
observe Wait, cancel their lifetime on failure, and always Close/join on exit.
Closing a server does not close the hub or affect recording.

`SocketPath() (string, error)` calls `ipc.SocketPath()`, removes a final `.sock`
if present, and appends `-voice.sock`. Thus `$XDG_RUNTIME_DIR/ginkgo.sock` becomes
`$XDG_RUNTIME_DIR/ginkgo-voice.sock`; the fallback `.../ginkgo/ipc.sock` becomes
`.../ginkgo/ipc-voice.sock`. Existing environment isolation is preserved. Hosts
may pass a different explicit path to `Listen` and `Watch` for tests.

The parent must exist, belong to the current effective UID, grant no group/other
permissions, and have no symlink components. Sockets are mode 0600. Refuse
unrelated files, symlinks, unsafe socket permissions/ownership, and active
listeners. Only an owned private socket that returns `ECONNREFUSED` is reclaimed
as stale, with an inode recheck. Shutdown unlinks only the original inode.
These checks rely on a private directory and do not protect against a hostile
process running as the same UID changing filesystem paths concurrently.

## Subscription and envelope

Connect using a Unix stream socket and **send zero bytes**. Any inbound byte
disconnects the client (one-byte fixed buffer, no subscribe parser). The first
line is a `state` snapshot inserted atomically before all subsequent observations.
It contains no old transcript or outcomes. Do not half-close the socket's write
side: EOF is treated as disconnection.

Every line is one JSON object plus newline, at most 65536 bytes including newline.

All envelope and live-state fields, `text`, and `final` must be present with
their declared JSON types, even when false, empty, or zero. Known optional fields
may be omitted but cannot be JSON `null` when supplied. Malformed observations
are rejected before reaching the client consumer.

```json
{"type":"state","schema_version":1,"epoch_id":"random-epoch","seq":0,"observed_at":"2026-10-17T12:00:00Z","listening":false,"recording_note":false,"active_note_session_id":"","text":"","final":false}
```

- Envelope: `type`, `schema_version` (1), `epoch_id`, `seq`, `observed_at` (UTC
  RFC3339 with fractional precision as needed). Timestamps describe publication,
  not audio time. `seq` increases per published event across all clients; gaps
  are allowed. Initial snapshots use the current sequence, including zero.
- Always explicit live flags: `listening`, `recording_note`,
  `active_note_session_id` (empty when not recording). A recording state requires
  a nonempty session ID; a nonrecording state requires an empty one.
- `state`: a current global state observation.
- `level`: optional normalized `level` (finite 0..1) and `speech` (boolean), at
  least one required. This describes capture activity, not note ownership.
- `transcript`: required `utterance_id`, positive `revision`, explicit `text`
  and `final` (these two fields also appear on other events). Each revision **replaces** the prior
  text for that utterance, rather than appending. Publishers must assign stable
  utterance IDs and increasing revisions and must not revise after `final:true`.
- Optional `audio_start_ms` and `audio_end_ms` are nonnegative offsets from the
  beginning of the utterance; when both are supplied end must be >= start.
- `note.saved`: required `note_session_id` and saved `note_id`.
  Sherpa AI-mode saves optionally include `processing:"ai"` for a validated AI
  draft or `processing:"raw_fallback"` with a nonempty `fallback_reason` for a
  raw save. Reasons are bounded application-owned diagnostics, never raw provider
  errors. AI disabled, provider failure/timeout, and invalid drafts follow the
  existing AI service's single-write raw fallback policy. Raw/legacy saves omit
  both fields; absence does not imply AI success. These additive v1 fields may be
  ignored by older clients. Captions remain ASR text, not the cleaned note body.
- `note.canceled`: required `note_session_id`.
- `note.error`: required `note_session_id` and bounded `error` string.
- `error`: required `error` string; optional attribution for capture/STT failures.

`note_session_id` is **result attribution**, independent of live
`active_note_session_id`. Set it from the utterance/capture's original note
session, never from whichever session is active when asynchronous STT finishes.
A final result can refer to an older session while `recording_note:false` or a
different session is currently active. An absent attribution means no note
session. Integrators must snapshot attribution before dispatching asynchronous
work. IDs are bounded to 256 UTF-8 bytes, text to 49152 bytes, error to 1024 bytes;
JSON escaping can further restrict text through the total message bound.
The hub validates bounds and required fields, not per-utterance revision history.
Publishers must avoid putting private provider diagnostics/secrets in errors.

## Backpressure, reconnects, client API

Defaults: 16 clients, 32 queued messages/client, two-second socket write deadline.
`Options{QueueSize, MaxClients}` accepts 1..1024 and 1..256 respectively; zero
selects defaults. Publication never performs socket IO or waits for clients.
Every queue overflow disconnects that subscriber, including partial/level events:
control/final loss can never leave the connection apparently healthy. A blocked
write may take up to the write deadline to close. Slow subscribers do not cancel
capture and cannot delay fast subscribers. No historical replay or delivery
acknowledgment is provided; disconnection requires a UI reset/reconnect, and
consumers needing durable outcomes must use ordinary note APIs.

`Watch(ctx, path, func(Event) error) error` validates bounded messages, v1
envelopes, initial state, and epoch/sequence ordering, returning on cancellation,
consumer error, malformed input or disconnection. It performs no reconnect.
The callback runs synchronously and must return promptly (honor `ctx` if it
blocks). Cancellation interrupts socket IO, not arbitrary callback/output writes.

`ginkgo-cli voice watch --output json` (JSON is also the default) subscribes
until cancellation, with no ordinary request timeout. Missing endpoints fail
clearly. Reconnecting apps must clear old transcript state on the first snapshot;
an epoch change also resets sequence/utterance identity. Preserve unknown fields
when relaying or ignore them when rendering; incompatible schemas require a new
version.
