#!/usr/bin/env python3
"""Isolated voice CLI smoke; real local STT requires --whisper, --model, --audio."""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import wave


REPO = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("ai_smoke", REPO / "scripts/smoke-ai-poc.py")
assert spec is not None and spec.loader is not None
helpers = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helpers)
TEXT = "  Exact spoken fixture.\n Preserve trailing spaces.  "


def write_config(path: Path, data: Path, whisper: Path, model: Path, enabled: bool) -> None:
    path.write_text(
        f"data_dir = {json.dumps(str(data))}\n"
        'namespace = "poc"\nhttp_addr = "127.0.0.1:0"\n'
        '[namespaces.poc]\ne2ee = false\n[ai]\nenabled = false\n'
        f'[voice]\nenabled = {str(enabled).lower()}\nmode = "raw"\n'
        '[voice.stt]\nresidency = "on_demand"\nlanguage = "en"\ndevice = "cpu"\n'
        f'executable = {json.dumps(str(whisper))}\nmodel = {json.dumps(str(model))}\n'
        'timeout = "5m"\n',
        encoding="utf-8",
    )
    path.chmod(0o600)


def write_fake_engine(path: Path, state: Path, calls: Path) -> None:
    path.write_text(
        f"#!{sys.executable}\n"
        "import json, pathlib, sys\n"
        f"with open({str(calls)!r}, 'a') as log: log.write('call\\n')\n"
        f"text = pathlib.Path({str(state)!r}).read_text(encoding='utf-8')\n"
        "prefix = sys.argv[sys.argv.index('-of') + 1]\n"
        "with open(prefix + '.json', 'w') as output:\n"
        "    json.dump({'result': {'language': 'en'}, 'transcription': [{'text': text}]}, output)\n",
        encoding="utf-8",
    )
    path.chmod(0o700)


def check_saved(result: dict, model: Path, expected: str | None) -> str:
    transcript = result["transcript"]
    text = transcript["text"]
    assert text.strip(), "empty transcript from speech sample"
    if expected is not None:
        assert text == expected, "transcript whitespace changed"
    entry = helpers.result_entry(result)
    assert entry["body"] == text, "saved body differs from exact transcript"
    assert entry["namespace"] == "poc"
    sources = entry["provenance"]["sources"]
    assert len(sources) == 1
    source = sources[0]
    assert source["original_text"] == text
    assert source["kind"] == "voice"
    assert source["stt_engine"] == transcript["engine"] == "whisper.cpp"
    assert source["stt_model"] == transcript["model"] == str(model)
    assert source.get("language") == transcript.get("language") == "en"
    assert source["id"] and source["captured_at"]
    return entry["id"]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, help="existing ginkgo-cli; otherwise build locally")
    parser.add_argument("--whisper", type=Path, help="installed whisper-cli executable")
    parser.add_argument("--model", type=Path, help="installed English-compatible GGML model")
    parser.add_argument("--audio", type=Path, help="explicit public PCM WAV speech sample")
    args = parser.parse_args()
    real = any(value is not None for value in (args.whisper, args.model, args.audio))
    if real and not all(value is not None for value in (args.whisper, args.model, args.audio)):
        parser.error("real mode requires --whisper, --model and --audio together")

    root = Path(tempfile.mkdtemp(prefix="ginkgo-voice-poc-", dir="/tmp/opencode"))
    root.chmod(0o700)
    daemon = None
    try:
        env = {key: value for key, value in os.environ.items() if not key.startswith("GINKGO_")}
        for key, name in (("XDG_RUNTIME_DIR", "run"), ("XDG_CONFIG_HOME", "config"),
                          ("XDG_DATA_HOME", "xdg-data"), ("XDG_STATE_HOME", "state"),
                          ("XDG_CACHE_HOME", "cache"), ("TMPDIR", "tmp")):
            directory = root / name
            directory.mkdir(mode=0o700)
            env[key] = str(directory)
        data = root / "data"
        data.mkdir(mode=0o700)
        binary = (args.binary or root / "ginkgo-cli").resolve()
        if args.binary is None:
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/ginkgo-cli"],
                           cwd=REPO, env=env, check=True, timeout=180)

        state, calls = root / "text", root / "engine-calls"
        state.write_text(TEXT, encoding="utf-8")
        calls.touch(mode=0o600)
        if real:
            whisper, model, audio = (p.resolve(strict=True) for p in (args.whisper, args.model, args.audio))
        else:
            whisper, model, audio = root / "whisper-cli", root / "ggml-fake.bin", root / "sample.wav"
            write_fake_engine(whisper, state, calls)
            model.write_bytes(b"lmgg" + b"\0" * 16)
            with wave.open(str(audio), "wb") as wav:
                wav.setnchannels(1)
                wav.setsampwidth(2)
                wav.setframerate(16000)
                wav.writeframes(b"\0\0" * 16000)

        config = root / "config.toml"
        write_config(config, data, whisper, model, True)
        daemon = helpers.start_daemon(binary, config, env, root / "daemon.log")

        def voice(*flags: str, success: bool = True) -> dict:
            result = subprocess.run(
                [str(binary), "--config", str(config), "voice", "transcribe", str(audio),
                 "--output", "json", *flags], cwd=REPO, env=env, text=True,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=540,
            )
            if not success:
                assert result.returncode != 0 and "disabled" in result.stderr.lower()
                return {}
            if result.returncode:
                raise RuntimeError(f"voice command failed: {result.stderr[-2000:]}")
            return json.loads(result.stdout)

        assert helpers.list_note_ids(binary, config, env) == set()
        preview = voice()
        assert preview["transcript"]["text"].strip() and not preview.get("entry")
        assert helpers.list_note_ids(binary, config, env) == set(), "preview saved a note"
        raw = voice("--save", "--mode", "raw")
        raw_id = check_saved(raw, model, None if real else TEXT)
        assert not raw.get("fallback_reason")
        assert helpers.list_note_ids(binary, config, env) == {raw_id}
        fallback = voice("--save", "--mode", "ai")
        fallback_id = check_saved(fallback, model, None if real else TEXT)
        assert fallback.get("fallback_reason"), "disabled AI did not report raw fallback"
        expected_ids = {raw_id, fallback_id}
        assert len(expected_ids) == 2
        assert helpers.list_note_ids(binary, config, env) == expected_ids

        if not real:
            state.write_text(" \n\t ", encoding="utf-8")
            empty = voice("--save")
            assert not empty.get("entry") and not empty["transcript"]["text"].strip()
            assert helpers.list_note_ids(binary, config, env) == expected_ids, "no-speech input saved"
            helpers.stop_daemon(daemon)
            daemon = None
            write_config(config, data, whisper, model, False)
            daemon = helpers.start_daemon(binary, config, env, root / "disabled-daemon.log")
            before = calls.read_bytes()
            voice("--save", success=False)
            assert calls.read_bytes() == before, "disabled voice launched engine"
            assert helpers.list_note_ids(binary, config, env) == expected_ids

        print(f"PASS: {'real local' if real else 'fake'} voice preview, raw save, AI-disabled fallback"
              + ("" if real else ", no speech, disabled-engine isolation"))
        print("All notes were confined to disposable storage; no remote/model-token calls.")
        return 0
    finally:
        helpers.stop_daemon(daemon)
        shutil.rmtree(root)


if __name__ == "__main__":
    raise SystemExit(main())
