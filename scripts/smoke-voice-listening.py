#!/usr/bin/env python3
"""Black-box listening smoke using synthetic PCM and isolated CLI/daemon storage."""

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
import time


REPO = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("voice_smoke", REPO / "scripts/smoke-voice-poc.py")
assert spec is not None and spec.loader is not None
voice = importlib.util.module_from_spec(spec)
spec.loader.exec_module(voice)
helpers = voice.helpers
TEXT = "  Exact microphone fixture.\n Preserve original spacing.  "


def wait_for(predicate, description: str, timeout: float = 10):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = predicate()
        if result:
            return result
        time.sleep(0.02)
    raise RuntimeError(f"timed out waiting for {description}")


def write_recorder(path: Path, mode: Path, marker: Path, ready: Path) -> None:
    path.write_text(
        f"#!{sys.executable}\n"
        "import os, pathlib, struct, sys, time\n"
        f"mode = pathlib.Path({str(mode)!r}).read_text().strip()\n"
        f"pathlib.Path({str(marker)!r}).write_text(str(os.getpid()))\n"
        "assert sys.argv[1:8] == ['--raw', '--format', 's16', '--rate', '16000', '--channels', '1']\n"
        "speech = struct.pack('<h', 6000) * 160\n"
        "quiet = bytes(320)\n"
        "frame = 0\n"
        "while True:\n"
        "    active = mode == 'speech' or (mode == 'once' and frame < 30) or (mode == 'twice' and (frame < 30 or 60 <= frame < 90))\n"
        "    os.write(1, speech if active else quiet)\n"
        "    frame += 1\n"
        f"    if frame == 25: pathlib.Path({str(ready)!r}).write_text('ready')\n"
        "    time.sleep(0.01)\n",
        encoding="utf-8",
    )
    path.chmod(0o700)


def write_engine(path: Path, text: Path, calls: Path, gate: Path, marker: Path) -> None:
    path.write_text(
        f"#!{sys.executable}\n"
        "import json, os, pathlib, sys, time\n"
        f"with open({str(calls)!r}, 'a') as log: log.write('call\\n')\n"
        f"pathlib.Path({str(marker)!r}).write_text(str(os.getpid()))\n"
        f"while pathlib.Path({str(gate)!r}).exists(): time.sleep(0.01)\n"
        f"text = pathlib.Path({str(text)!r}).read_text()\n"
        "prefix = sys.argv[sys.argv.index('-of') + 1]\n"
        "with open(prefix + '.json', 'w') as out:\n"
        "    json.dump({'result': {'language': 'en'}, 'transcription': [{'text': text}]}, out)\n",
        encoding="utf-8",
    )
    path.chmod(0o700)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, help="existing binary; otherwise build locally")
    args = parser.parse_args()
    root = Path(tempfile.mkdtemp(prefix="ginkgo-listening-smoke-", dir="/tmp/opencode"))
    daemon = None
    cli = None
    try:
        env = {k: v for k, v in os.environ.items() if not k.startswith("GINKGO_")}
        for key, name in (("XDG_RUNTIME_DIR", "run"), ("XDG_CONFIG_HOME", "config"),
                          ("XDG_DATA_HOME", "xdg-data"), ("XDG_STATE_HOME", "state"),
                          ("XDG_CACHE_HOME", "cache"), ("TMPDIR", "tmp")):
            directory = root / name
            directory.mkdir(mode=0o700)
            env[key] = str(directory)
        binary = (args.binary or root / "ginkgo-cli").resolve()
        if args.binary is None:
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/ginkgo-cli"],
                           cwd=REPO, env=env, check=True, timeout=180)
        data = root / "data"
        data.mkdir(mode=0o700)
        model, engine, recorder = root / "model.bin", root / "whisper-cli", root / "pw-record"
        text, calls = root / "text", root / "engine-calls"
        gate, engine_marker = root / "block-engine", root / "engine-pid"
        mode, marker, ready = root / "mode", root / "capture-pid", root / "ready"
        model.write_bytes(b"lmggfixture")
        text.write_text(TEXT)
        calls.touch(mode=0o600)
        write_engine(engine, text, calls, gate, engine_marker)
        write_recorder(recorder, mode, marker, ready)
        config = root / "config.toml"

        def configure(enabled=True):
            voice.write_config(config, data, engine, model, enabled)
            with config.open("a") as out:
                out.write(f'\n[voice.capture]\nexecutable = {json.dumps(str(recorder))}\n'
                          'silence = "200ms"\nmax_duration = "4s"\nstart_timeout = "2s"\n')

        def capture_mode(value):
            mode.write_text(value)
            marker.unlink(missing_ok=True)
            ready.unlink(missing_ok=True)

        def command(*flags, output="json"):
            out = helpers.cli_run(binary, config, env, ["voice", *flags, "--output", output])
            return json.loads(out) if output == "json" else out

        def status():
            return command("status")["voice_status"]

        def notes():
            return helpers.list_note_ids(binary, config, env)

        def capture_pid():
            return int(wait_for(lambda: marker.read_text() if marker.exists() else None, "recorder PID"))

        def clean_capture(pid):
            helpers.wait_for_pid_exit(pid, 5)
            assert not list((root / "tmp").glob("ginkgo-capture-*")), "temporary audio survived cleanup"

        configure()
        daemon = helpers.start_daemon(binary, config, env, root / "daemon.log")
        assert not status()["active"] and notes() == set()

        capture_mode("quiet")
        empty = command("listen", "--start-timeout", "200ms", "--save")
        assert empty["transcript"]["text"] == "" and not empty.get("entry")
        clean_capture(capture_pid())
        assert notes() == set()

        capture_mode("once")
        assert command("listen", output="text") == TEXT
        clean_capture(capture_pid())
        assert notes() == set(), "transcript preview saved a note"

        capture_mode("once")
        saved = command("listen", "--save", "--mode", "raw")
        first = voice.check_saved(saved, model, TEXT)
        clean_capture(capture_pid())
        assert notes() == {first}

        capture_mode("once")
        fallback = command("record", "--save", "--mode", "ai", "--new")
        second = voice.check_saved(fallback, model, TEXT)
        assert fallback["fallback_reason"]
        expected = {first, second}

        capture_mode("twice")
        enabled = command("enable", "--mode", "raw")
        assert enabled["voice_status"]["active"]
        pid = capture_pid()
        wait_for(lambda: status()["segments"] == 2, "two background utterances")
        assert status()["active"] and helpers.pid_is_alive(pid), "enable died with its caller"
        command("disable")
        clean_capture(pid)
        after = notes()
        assert len(after - expected) == 2
        expected = after

        capture_mode("speech")
        command("enable")
        pid = capture_pid()
        wait_for(ready.exists, "qualified speech")
        command("cancel")
        clean_capture(pid)
        assert notes() == expected, "canceled partial speech saved a note"

        capture_mode("speech")
        command("enable")
        pid = capture_pid()
        wait_for(ready.exists, "speech to finalize")
        command("stop")
        clean_capture(pid)
        after = notes()
        assert len(after - expected) == 1, "stop did not finalize current utterance"
        expected = after

        capture_mode("speech")
        cli = subprocess.Popen([str(binary), "--config", str(config), "voice", "listen", "--save"],
                               cwd=REPO, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        pid = capture_pid()
        wait_for(ready.exists, "one-shot speech before disconnect")
        helpers.reap_cli_process(cli)
        cli = None
        clean_capture(pid)
        wait_for(lambda: not status()["active"], "one-shot cancellation")
        assert notes() == expected, "disconnected listen saved a note"

        capture_mode("once")
        engine_marker.unlink(missing_ok=True)
        gate.touch()
        cli = subprocess.Popen([str(binary), "--config", str(config), "voice", "listen", "--save", "--mode", "ai"],
                               cwd=REPO, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        engine_pid = helpers.wait_for_provider_pid(engine_marker, cli, 10)
        helpers.reap_cli_process(cli)
        cli = None
        helpers.wait_for_pid_exit(engine_pid, 5)
        wait_for(lambda: not status()["active"], "inference cancellation")
        clean_capture(capture_pid())
        gate.unlink()
        assert notes() == expected, "canceled inference saved a fallback note"

        capture_mode("speech")
        command("enable")
        pid = capture_pid()
        wait_for(ready.exists, "speech before daemon shutdown")
        helpers.stop_daemon(daemon)
        daemon = None
        clean_capture(pid)

        configure(False)
        daemon = helpers.start_daemon(binary, config, env, root / "disabled.log")
        capture_mode("once")
        before = calls.read_bytes()
        result = subprocess.run([str(binary), "--config", str(config), "voice", "enable"],
                                cwd=REPO, env=env, capture_output=True, text=True, timeout=10)
        assert result.returncode != 0 and "disabled" in result.stderr.lower()
        assert not marker.exists() and calls.read_bytes() == before
        assert notes() == expected
        print("PASS: no-speech, preview, raw/AI-fallback save, detached continuous capture, stop, cancel, disconnect, shutdown, disabled isolation")
        print("Synthetic PCM only; no microphone, provider call, user config, or installed service touched.")
        return 0
    finally:
        if cli is not None:
            helpers.reap_cli_process(cli)
        helpers.stop_daemon(daemon)
        shutil.rmtree(root)


if __name__ == "__main__":
    raise SystemExit(main())
