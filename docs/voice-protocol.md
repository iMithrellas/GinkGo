# Voice protocols

Public voice events use a Unix socket; commands to the native helper use inherited
stdio. The Go definitions are in
[`internal/voiceevents`](../internal/voiceevents) and
[`internal/voiceworker`](../internal/voiceworker).

## Public events socket: NDJSON v1

The optional `voice.events.enabled` endpoint is read-only. A connection sends no
bytes; its first received line is an atomic current-state snapshot, followed by
new events. Any client input, EOF half-close, malformed line, or disconnect ends
the subscription. The endpoint never starts capture, loads models, changes
notes, or returns audio. It does expose transcript text, including ambient
captions while saving is off. Treat the socket, consumer logs, and retained text
as sensitive. It keeps no transcript history.

Each UTF-8 line is one JSON object, newline terminated, at most 65,536 bytes
including the newline. The envelope and live-state keys below are required and
must have their declared JSON types, including explicit false, empty, and zero
values. Known optional fields may be omitted, but cannot be null. Unknown fields
are ignored for additive v1 evolution.

```json
{"type":"state","schema_version":1,"epoch_id":"epoch","seq":0,"observed_at":"2026-10-17T12:00:00Z","listening":false,"recording_note":false,"active_note_session_id":"","text":"","final":false}
```

`observed_at` is UTC RFC3339 publication time. Sequence increases
for published events; gaps are allowed. `epoch_id` changes on daemon restart.
`active_note_session_id` is nonempty only while recording a note.

| Type | Required event data | Meaning |
| --- | --- | --- |
| `state` | Live-state fields | Current listening and note-recording state. |
| `level` | `level` and/or `speech` | Optional finite level from 0 to 1 and/or boolean activity. |
| `transcript` | `utterance_id`, positive `revision`, `text`, `final` | Each revision replaces prior text; never append revisions or revise after final. |
| `note.saved` | `note_session_id`, `note_id` | A saved result. Optional AI `processing` and bounded `fallback_reason` describe postprocessing. |
| `note.canceled` | `note_session_id` | A selected note did not save. |
| `note.error` | `note_session_id`, bounded `error` | A note workflow error. |
| `error` | bounded `error` | Capture/STT or endpoint error; attribution may be present. |

All events carry `type`, `schema_version:1`, `epoch_id`, `seq`, `observed_at`,
the three live-state fields, `text`, and `final`, even where empty or false.
Optional audio offsets `audio_start_ms` and `audio_end_ms` are nonnegative
sample-derived offsets; when both exist end is not before start. Origins differ
by backend, so do not compare them across streams or interpret them as wall time
or phonetic boundaries. IDs are limited to 256 UTF-8 bytes, text to 49,152 bytes,
and errors to 1,024 bytes; JSON encoding must also fit the line bound.

`note_session_id` attributes a result to its originating session. Async work must
snapshot it before dispatch. A final may belong to a prior session
after recording has stopped or another session has begun. Captions and public
IDs are observations, never save authorization.

Publication does not wait for socket writes. Defaults are 16 clients, 32 queued
messages per client, and a two-second write deadline. Queue overflow disconnects
the slow client, even if the dropped message was a level or final; there is no
acknowledgement or replay. Reconnect and reset rendered state from the first
snapshot, and reset identity on epoch change. `Watch` validates framing and
ordering but does not reconnect automatically; callbacks should return promptly.
Consumers requiring durable outcomes must use note APIs.

The default path is the IPC socket path with `.sock` replaced by `-voice.sock`.
The private parent directory must be owned by the effective UID and inaccessible
to group/other users, without symlink components; socket mode is 0600. Only an
owned stale socket is reclaimed. These checks do not defend against hostile
same-UID processes racing filesystem operations.

## Private helper: stdio v1

The supervisor starts the installed helper without a shell. Inherited stdin and
stdout carry UTF-8 NDJSON; stdout is protocol only. Each message is at most
65,536 bytes excluding newline. Stderr is discarded and capped at 64 KiB; it is
never returned in errors. The helper must remain in the supervisor's process
group. See [helper trust](internals.md#capture-and-native-trust) for its privileges
and environment.

The exchange is `init` → `ready` → `start` → events → `stop` → `stopped`.
After `ready`, the parent sends control messages such as:

```json
{"version":1,"type":"start","epoch":"run-1"}
{"version":1,"type":"stop","epoch":"run-1"}
```

`init.config` must include every non-optional field defined by
[`voiceworker.Config`](../internal/voiceworker/protocol.go). Init validates
trusted absolute paths and integer-millisecond settings without exposing paths
in diagnostics. Audio is 16 kHz mono signed little-endian PCM16. Update, window,
silence, maximum recording, and trigger timeout are 1..3,600,000 ms; cooldown
0..60,000 ms; pre-roll 0..30,000 ms. Window must be at least update, silence no
greater than maximum recording, and the selected label at most 128 bytes.

The strict decoder rejects missing fields, nulls, duplicate/unknown or
case-aliased fields, arrays, invalid UTF-8, and trailing JSON. Commands require
protocol version 1 and a 1..128 character ASCII identifier epoch. Init occurs
once. The helper initializes models before `ready`, without opening capture.
Only `start` authorizes microphone access; stop before start must not open it.
Stop is valid during initialization. EOF before the stop command aborts the job.
After stop, EOF completes the control barrier; a second command,
malformed/unterminated frame, cancellation, or startup failure revokes pending
work. Graceful stop drains eligible final results; abort/cancel does not save
partials. Startup defaults to 30 seconds and shutdown to five seconds (each
configurable up to five minutes). Once stop is observed, startup timeout no
longer applies. Continuing PCM keeps capture live through silence.

Provider policy and actual attempt are distinct. `auto` prefers CUDA; CPU
fallback requires explicit `allow_cpu_fallback`. Before any validated `ready`,
the supervisor may retry once as CPU after process/transport failure or ready
timeout, with a fresh epoch and `fallback_reason:"cuda_init_failed"`. It never
retries malformed protocol or after ready, stop, or cancellation. Ready reports
requested/effective provider and only bounded reasons (`cuda_unavailable`,
`cuda_failed`, or retry reason). Effective CUDA reports execution-provider
initialization; individual graph nodes may still run on CPU. Never include native
error text or paths.

Every child event carries required `version:1`, `type`, matching `epoch`,
consecutive `sequence` starting at 1, and `recording_note`; the first event is
`ready`. Each type has only its defined payload:

| Type | Payload | Contract |
| --- | --- | --- |
| `ready` | provider status | Once, before capture, recording false. |
| `state` | optional `session_id` | Recording transition; finalization may remain pending after state off. |
| `level` | `level` | Finite 0..1. |
| `wake` | session, utterance, audio span | Acoustic selection starts; unique IDs for this epoch. |
| `transcript` | transcript | Revisable ambient caption, never save authority. |
| `note_candidate` | candidate | Sole potentially persistable selected final. |
| `note_canceled` | session, code | Selected result canceled (`timeout`, `empty`, `control_only`, `user`, `shutdown`). |
| `error` | code | Terminal: `capture_failed`, `model_failed`, `inference_failed`, `provider_unavailable`, `internal`. |
| `stopped` | none | Terminal, no selection pending. |

Spans are half-open integer sample offsets from this helper epoch; valid spans
have nonnegative endpoints at most 2^53 and positive length. Wake and candidate selected span
length cannot exceed `max_recording_ms * 16` samples, including keyword,
pre-roll, pause, and dictation. Candidate must match the pending wake IDs and
start sample, and cover at least its wake end. One pending selected session is
resolved once by candidate or cancellation. Ambient transcript IDs/revisions
are separate from selected IDs; transcripts cannot be revised after final.
Text is UTF-8, NUL-free, at most 16 KiB. History is capped at 4,096 utterances
and sessions per epoch.

Candidate `original` is exact selected-audio recognition. `body` must equal it
unless a validated control prefix has a matching audio span and exact text
prefix; uncertain boundaries must not be removed. The current native adapter
does not claim a phrase-end boundary, so it emits identical original/body.
The parent supplies trusted engine metadata. The daemon also
requires recording opt-in and validates the candidate before persistence.

The output queue is bounded to eight lines plus one callback delivery. A slow
consumer must not block stop/deadlines; callback errors propagate, while arbitrary
stuck Go callback code cannot be forcibly canceled. Cancellation kills the helper
process group and waits for direct children. Escaped process groups remain
outside the contract.
