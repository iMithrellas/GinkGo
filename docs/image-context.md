# Screenshot and image context

GinkGo can attach PNG/JPEG originals to an AI note and include them in a
vision-capable provider request. Automatic capture is optional; explicit
attachments work independently of that setting.

## Automatic capture on “Hey Ginkgo”

```toml
[voice.screenshot]
automatic = true
mode = "screen"                 # window | screen | all-screens
backend = "auto"                # auto | hyprland
```

This applies to **saved Sherpa AI notes**, for example:

```sh
ginkgo-cli voice stream --save --mode ai
```

On an accepted wake, capture starts asynchronously and belongs to that specific
note session. Speech processing continues while capture runs. Capture has a
five-second deadline. Wake-only/canceled sessions discard their captures; a
capture failure allows the dictated note to proceed without images. Caption-only
and raw streams do not capture screenshots. Automatic capture defaults to off.

| Mode | Hyprland behavior |
| --- | --- |
| `window` | Focused window's visible rectangle, using global logical coordinates |
| `screen` | Monitor containing the focused window; compositor-focused monitor when there is no active window |
| `all-screens` | One image per enabled monitor, in compositor-reported order |

Hyprland is the initial backend and requires `hyprctl` and `grim`. Automatic
capture needs the daemon to inherit the graphical session's Wayland/Hyprland
environment; launching it from session startup is one way to arrange this.
Explicit `context capture` runs in the CLI's graphical session. `auto` currently
detects Hyprland through `HYPRLAND_INSTANCE_SIGNATURE`.

Window mode captures visible screen pixels, including overlapping content in that
rectangle. Multi-monitor captures are sequential, rather than an atomic snapshot
of every monitor. An unavailable target/mode is reported instead of expanding the
capture to a broader target.

## Explicit attachments

Attach images directly to a typed note:

```sh
printf '%s\n' 'Document the error in the attached screenshot.' |
  ginkgo-cli ai note --stdin --image error.png
```

Or stage context for the next AI note in a namespace:

```sh
ginkgo-cli context attach error.png diagram.jpg --namespace work
ginkgo-cli context capture --mode window --namespace work
ginkgo-cli context status --namespace work
ginkgo-cli context clear --namespace work
```

Staging replaces that namespace's previous pending batch and expires after two
minutes. It is consumed once: at the next saved Sherpa AI wake, AI note request,
or saved Whisper AI transcription. A staged batch takes precedence over automatic
capture. Direct `ai note --image` input takes precedence over, and consumes, any
staged batch. A canceled attempt discards its pending input. Pending images are
memory-only and are cleared on daemon shutdown.

These commands work when `voice.screenshot.automatic = false`. Explicit originals
are retained with a saved note even if AI is disabled or image input is skipped.
Automatic capture itself is skipped unless image support is known to be available.

## Model and adapter support

Both the selected **model** and the **adapter** must support image input.
Unknown capability is treated as unavailable; text processing continues.

```toml
[ai.providers.vision]
type = "openai-compatible"
base_url = "https://your-endpoint.example/v1"
model = "your-vision-model"
api_key_env = "GINKGO_MODEL_API_KEY"
image_input = "auto"            # auto | supported | unsupported
```

- **OpenAI-compatible:** image content parts are supported. `auto` checks
  `GET /models/{model}` for explicit input-modality or vision metadata matching
  that exact model ID. These metadata extensions are not standardized: ordinary
  OpenAI model listings often contain no such information. Set
  `image_input = "supported"` when the selected model's documented capabilities
  confirm image support; update that declaration when changing models.
- **CLI wrappers:** `supported` explicitly declares that the wrapper and its
  selected model accept the extended request's `images` array. Each image carries
  base64 `data`, `media_type`, dimensions, capture time, and a short label. `auto`
  is unknown for generic wrappers.
- **OpenCode / OpenCode session:** the generation-only endpoints currently accept
  only a text `prompt`. Images are skipped even when the underlying model supports
  vision or `image_input` is declared supported. The current contracts are listed
  in the [V2 API reference](https://opencode.ai/v2/docs/api/).

Text-only requests do not probe image capabilities. Capability probing sends no
note or screenshot content. Explicit attachments still persist locally when
unsupported; `image_status` in AI results and `note.saved` voice events describes
whether images were included in the request or skipped. Automatic capability
skips and capture failures are reported through the voice events stream.

## Prompt-controlled use

Images are sent alongside the note in one multimodal request. The prompt tells
the model to use them **only when the current dictated/typed body asks for visual
context**—for example, “this error on my screen” or “the attached diagram.”
Otherwise it must ignore them, including for tags, spelling correction, and
target selection. Existing notes and text inside screenshots cannot authorize
their use. This image policy is also appended to custom system prompts when
images are supplied.

This is a rule about model behavior, not conditional upload: an included image
is sent to the configured provider even if the model ultimately ignores it.

## Originals, inspection, and sync

Saved notes retain image references in the original input's provenance. Original
bytes live separately in `data_dir/attachments`, with private file permissions
and SHA-256 integrity checks. Images are preserved through raw AI fallback and
append-only expansions. Earlier notes' images are not automatically resent as
context for a later request. Images are limited to eight per request, 8 MiB each,
12 MiB total, and 32 million pixels per image; PNG and JPEG are supported.

```sh
ginkgo-cli note images NOTE_ID --namespace work
ginkgo-cli note images NOTE_ID --namespace work --directory ./original-images
```

The second command copies verified originals using attachment IDs as filenames;
it never overwrites existing files. JSON note exports contain image references;
copy originals separately when making an offline export or backup.

Attachments follow normal note synchronization. Separate opaque blob transfers
upload each referenced image once; note edits only resend references. Images use
the namespace's existing encryption policy, including client-side encryption for
E2EE namespaces. The server stores opaque payloads in `data_dir/replication-blobs`.
Both clients and the server need attachment support.

Note text is available before image downloads finish. Failed downloads leave the
pull cursor unchanged for retry, and `note images --directory` reports originals
that are not available locally. Referenced bytes are verified after decryption.
Local images and server blobs remain retained while replication history may
reference them; automatic history compaction/garbage collection is not implemented.
Cancellation before any note write is attempted removes newly stored originals;
ambiguous write failures retain them to avoid removing a committed note's source.

## Extension points

- `imagecontext.Capturer` and `Resolver`: platform selection and supported modes.
- `imagecontext.CommandRunner`: desktop command execution and deterministic tests.
- `ai.CapabilityProvider`: effective selected-model/adapter capabilities.
- `imagecontext.Store` and `blobstore.Store`: original-image and opaque-blob storage.
- `sync.BlobRemote`: attachment transport independently of note replication.

Capture/clipboard/platform backends can be added without changing note generation,
and provider adapters can add image support independently of desktop capture.
