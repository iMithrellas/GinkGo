# Microphone listening

GinkGo can keep a PipeWire microphone stream open in the daemon and transcribe
utterances after silence, or capture one utterance on demand. Voice is opt-in;
raw dictation works with AI disabled. This is dictation, without wake words or
spoken command routing.

## Setup

Use Linux with a working PipeWire session, the installed `pw-record` executable,
and the [Whisper configuration](voice-ai-poc.md#start-with-raw-local-voice-transcription).
The Go binary remains CGO-free; PipeWire and whisper.cpp are external programs.
Merge into your GinkGo config and restart your daemon after editing configuration:

```toml
[voice]
enabled = true
mode = "raw"

[voice.capture]
executable = "pw-record"
target = ""                    # empty uses PipeWire's default capture source
silence = "1.5s"
max_duration = "2m"             # per utterance, including pre-roll and silence
start_timeout = "30s"           # initial speech wait, one-shot only
threshold = 0.02                # normalized RMS energy, (0, 1]
```

The default `voice.enabled = false` prevents microphone/engine probes and
launches. `voice enable` requires this opt-in; it changes runtime listening state,
not the configuration file. A daemon restart starts with listening off.

## Commands

```sh
# One utterance; end after two seconds of silence, print exact transcript.
ginkgo-cli voice listen --silence 2s

# Save that utterance, or use record (an alias of listen).
ginkgo-cli voice record --silence 2s --save --mode raw --output json

# Continuous dictation: save each completed utterance in this namespace.
ginkgo-cli voice enable --namespace work --silence 2s --mode raw
ginkgo-cli voice status --output json

# Finish active speech and drain completed utterances, then release the mic.
ginkgo-cli voice stop

# Discard unfinished/queued work and cancel active inference; release the mic.
ginkgo-cli voice disable
# voice cancel has the same cancellation behavior.
```

One-shot listens have `--save` opt-in. Continuous enable always saves, survives
the enabling CLI's exit, and runs until stop, disable, cancellation, capture or
processing failure, or daemon shutdown. Only one microphone job may be active;
another enable/listen returns a busy error. Repeated disable/cancel is harmless.

`--device` selects a PipeWire target. `--silence`, `--threshold`, and
`--max-duration` override capture settings for this job. `--start-timeout` applies
only to one-shot waiting for initial speech. `--timeout` bounds the CLI request;
it does not set the lifetime of continuous listening. A timed-out stop request
can leave the daemon draining: inspect status or use disable to cancel it.

Both capture modes support `--mode raw|ai`, `--provider`, `--title`, `--tag`,
`--model`, `--language`, and `--namespace`. AI `--new`/`--note ID` require saving
in AI mode. Raw mode never invokes AI. AI failures preserve the original transcript
through raw fallback, and saved entries preserve voice provenance. Remote AI
receives the selected transcript and bounded note/tag context; sync encryption
does not protect this separate request.

AI mode asks the model to correct likely transcription errors in jargon,
technical names and acronyms using the supplied context (for example, a database
discussion's "post gress" → "PostgreSQL"). Uncertain names should be retained.
The saved AI note can use corrected terminology while `note show NOTE_ID --original`
still displays the exact transcript. Raw mode and raw fallback retain the transcript
verbatim; terminology correction quality depends on the selected model.

## Endpointing and resource bounds

- RMS energy is measured in 10ms frames, with up to 200ms pre-roll and 150ms of
  above-threshold audio required. Background noise can qualify; adjust threshold
  to your microphone. This is energy-based endpointing, not semantic VAD.
- No initial speech before `start_timeout` produces a successful empty transcript
  and no note. An empty STT transcript also creates no note.
- Silence is positive and at most 1m. Maximum utterance duration is 150ms–5m;
  the sample cap rounds down to 10ms frames. Initial wait is positive and at most
  5m. Continuous silence has no initial-wait limit.
- At maximum duration, qualifying speech is finalized; continuous capture starts
  another segment on the same stream. One-shot returns after its first segment.
- Capture remains live during inference. Four completed segments may wait behind
  one processing segment. Ordinary queue overflow fails the job explicitly and
  cancels pending work instead of silently dropping speech. Stop's final flush
  waits for queue space and can be interrupted by disable/cancel.
- STT remains serialized and on demand; the model is released after each
  transcription. There is no persistent-model residency yet.
- Temporary WAVs are private and removed after processing/cancellation. Raw audio
  is not retained in notes. Recorder children and their process group are cleaned
  up before stop/disable acknowledges completion.
- EOF/device loss or five seconds without a complete PCM frame faults the job.
  Inspect `voice status`, repair the source, then enable again. Automatic reconnect
  and suspend recovery are not implemented.

Canceling a one-shot request (including disconnect) discards unfinished work.
Disable/cancel stops further writes before acknowledging; it does not delete
notes already committed by previous utterances. Shutdown joins capture and
processing before releasing storage.

## Status and verification

JSON responses contain `voice_status`; one-shot additionally contains `transcript`
and optionally `entry`, `action`, and `fallback_reason`. Status `enabled` means
the configured voice feature is allowed, while `active` means a job is running.
`continuous` identifies the active continuous job. `state`, `last_error`,
`segments` (successfully processed utterances, including empty transcripts), and
`last_note_id` describe the latest job. Status does not retain transcript/audio
history. Duration fields are integer nanoseconds.

Routine checks use fake devices, synthetic PCM, and fake STT:

```sh
go test ./...
go vet ./...
go test -race ./internal/voice ./internal/daemon ./internal/ipc ./internal/cli
python3 -B scripts/smoke-voice-listening.py
```

The smoke test owns a disposable config/database/socket/daemon. It covers
no-speech, preview, saving, AI-disabled fallback, continuous listening after the
CLI exits, stop/cancel, disconnect, shutdown, and disabled-feature isolation.
It never opens a real microphone or calls a model provider. Real microphone
quality, suspend, and hardware compatibility still need opt-in manual validation.
The capture argv was also checked against `pw-record --help` from PipeWire 1.6.9;
that check validates flag availability, not microphone behavior.
