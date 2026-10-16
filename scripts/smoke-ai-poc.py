#!/usr/bin/env python3
"""Black-box AI note smoke test using an isolated daemon and fake provider."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time


REPO = Path(__file__).resolve().parents[1]
CREATE_BODY = "Cleaned onboarding summary."
EXPANSION_ADDITION = "Added deployment follow-up."


def toml_string(value: str) -> str:
    return json.dumps(value)


def write_config(path: Path, data_dir: Path, wrapper: Path, enabled: bool) -> None:
    path.write_text(
        "\n".join(
            [
                f"data_dir = {toml_string(str(data_dir))}",
                'namespace = "poc"',
                'http_addr = "127.0.0.1:0"',
                "",
                "[namespaces.poc]",
                "e2ee = false",
                "",
                "[ai]",
                f"enabled = {str(enabled).lower()}",
                'provider = "fixture"',
                'timeout = "10s"',
                "",
                "[ai.notes]",
                "context_limit = 8",
                "",
                "[ai.providers.fixture]",
                'type = "cli"',
                f"executable = {toml_string(str(wrapper))}",
                "",
            ]
        ),
        encoding="utf-8",
    )
    path.chmod(0o600)


def write_fake_provider(path: Path) -> None:
    source = r'''#!/usr/bin/env python3
import json
import os
import sys
import time

request = json.load(sys.stdin)
with open(os.environ["GINKGO_POC_CALL_LOG"], "a", encoding="utf-8") as log:
    log.write("called\n")
with open(os.environ["GINKGO_POC_STATE"], encoding="utf-8") as state_file:
    state = json.load(state_file)
mode = state.get("mode", "create")
if mode == "invalid":
    text = "not a valid note draft"
elif mode == "block":
    with open(state["pid_marker"], "w", encoding="utf-8") as marker:
        marker.write(str(os.getpid()))
    time.sleep(60)
    raise SystemExit("block fixture unexpectedly completed")
elif mode == "expand":
    draft = {
        "action": "expand_note",
        "target_id": state["target_id"],
        "title": "Cleaned onboarding summary",
        "body": "Added deployment follow-up.",
        "tags": ["onboarding"],
    }
    text = json.dumps(draft)
else:
    draft = {
        "action": "create_note",
        "title": "Cleaned onboarding summary",
        "body": "Cleaned onboarding summary.",
        "tags": ["onboarding"],
    }
    text = json.dumps(draft)
json.dump({"text": text}, sys.stdout, separators=(",", ":"))
'''
    path.write_text(source, encoding="utf-8")
    path.chmod(0o700)


def cli_run(
    binary: Path,
    config: Path,
    env: dict[str, str],
    args: list[str],
    stdin: str | None = None,
) -> str:
    result = subprocess.run(
        [str(binary), "--config", str(config), *args],
        cwd=REPO,
        env=env,
        input=stdin,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=45,
        check=False,
    )
    if result.returncode:
        # Inputs are synthetic. Bound diagnostics and never echo provider requests.
        stderr = result.stderr[-4000:]
        raise RuntimeError(
            f"command failed ({result.returncode}): {args[0]} {args[1:]}\n{stderr}"
        )
    return result.stdout


def json_command(*args: str, **kwargs: object) -> dict:
    output = cli_run(*args, **kwargs)  # type: ignore[arg-type]
    try:
        value = json.loads(output)
    except json.JSONDecodeError as exc:
        raise RuntimeError("command stdout was not valid JSON") from exc
    if not isinstance(value, dict):
        raise RuntimeError("expected a JSON object on stdout")
    return value


def result_entry(result: dict) -> dict:
    entry = result.get("entry")
    if not isinstance(entry, dict):
        raise RuntimeError("AI note response did not contain an entry")
    return entry


def list_note_ids(binary: Path, config: Path, env: dict[str, str]) -> set[str]:
    output = cli_run(binary, config, env, ["note", "list", "--output", "json"])
    try:
        entries = json.loads(output)
    except json.JSONDecodeError as exc:
        raise RuntimeError("note list stdout was not valid JSON") from exc
    if not isinstance(entries, list):
        raise RuntimeError("note list JSON was not an array")
    return {entry["id"] for entry in entries}


def pid_is_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    try:
        stat = Path(f"/proc/{pid}/stat").read_text(encoding="utf-8")
        state = stat[stat.rfind(")") + 2 :].split()[0]
        return state != "Z"
    except (FileNotFoundError, IndexError):
        return True


def wait_for_provider_pid(marker: Path, cli: subprocess.Popen, timeout: float) -> int:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if marker.is_file():
            try:
                return int(marker.read_text(encoding="utf-8").strip())
            except ValueError as exc:
                raise RuntimeError("fake provider wrote an invalid PID marker") from exc
        if cli.poll() is not None:
            raise RuntimeError("AI CLI exited before fake provider started")
        time.sleep(0.05)
    raise RuntimeError("timed out waiting for fake provider start marker")


def reap_cli_process(process: subprocess.Popen) -> tuple[str, str]:
    if process.poll() is None:
        process.terminate()
    try:
        return process.communicate(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        return process.communicate(timeout=5)


def wait_for_pid_exit(pid: int, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if not pid_is_alive(pid):
            return
        time.sleep(0.05)
    raise RuntimeError(f"fake provider process {pid} survived CLI cancellation")


def start_daemon(binary: Path, config: Path, env: dict[str, str], log: Path) -> subprocess.Popen:
    socket = Path(env["XDG_RUNTIME_DIR"]) / "ginkgo.sock"
    # A prior owned daemon may leave its socket pathname after shutdown.
    if socket.exists():
        socket.unlink()
    stream = log.open("w", encoding="utf-8")
    try:
        process = subprocess.Popen(
            [str(binary), "--config", str(config), "daemon"],
            cwd=REPO,
            env=env,
            stdin=subprocess.DEVNULL,
            stdout=stream,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    finally:
        stream.close()

    try:
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if process.poll() is not None:
                raise RuntimeError(f"isolated daemon exited early; see {log}")
            if socket.is_socket():
                return process
            time.sleep(0.05)
        raise RuntimeError(f"timed out waiting for isolated daemon socket; see {log}")
    except Exception:
        stop_daemon(process)
        raise


def stop_daemon(process: subprocess.Popen | None) -> None:
    if process is None:
        return
    if process.poll() is None:
        # The daemon has its own session; signal only its owned process group.
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=5)


def write_fixture_state(
    path: Path, mode: str, target_id: str = "", pid_marker: str = ""
) -> None:
    state = {"mode": mode, "target_id": target_id, "pid_marker": pid_marker}
    path.write_text(json.dumps(state), encoding="utf-8")
    path.chmod(0o600)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--binary",
        type=Path,
        help="use an existing ginkgo-cli binary instead of building one",
    )
    args = parser.parse_args()

    root = Path(tempfile.mkdtemp(prefix="ginkgo-ai-poc-", dir="/tmp/opencode"))
    root.chmod(0o700)
    daemon: subprocess.Popen | None = None
    try:
        runtime = root / "run"
        data = root / "data"
        runtime.mkdir(mode=0o700)
        data.mkdir(mode=0o700)
        binary = (args.binary or (root / "ginkgo-cli")).resolve()
        if args.binary is None:
            subprocess.run(["go", "build", "-o", str(binary), "./cmd/ginkgo-cli"], cwd=REPO, check=True)
        elif not binary.is_file():
            raise RuntimeError(f"binary not found: {binary}")

        wrapper = root / "fake-provider"
        write_fake_provider(wrapper)
        config = root / "config.toml"
        write_config(config, data, wrapper, enabled=True)
        calls = root / "provider-calls.log"
        calls.touch(mode=0o600)
        # Ignore user GINKGO_* overrides so config, remotes, and namespace stay isolated.
        env = {key: value for key, value in os.environ.items() if not key.startswith("GINKGO_")}
        env["XDG_RUNTIME_DIR"] = str(runtime)
        env["GINKGO_POC_CALL_LOG"] = str(calls)
        state = root / "fixture-state.json"
        write_fixture_state(state, "create")
        env["GINKGO_POC_STATE"] = str(state)

        daemon = start_daemon(binary, config, env, root / "daemon.log")
        original_create = "ORIGINAL_ONLY_CREATE: typed input must not enter cleaned body."
        created = json_command(
            binary,
            config,
            env,
            ["ai", "note", "--stdin", "--new", "--output", "json"],
            original_create,
        )
        entry = result_entry(created)
        note_id = entry["id"]
        if entry.get("body") != CREATE_BODY:
            raise RuntimeError("create did not save the fixture's cleaned body")
        sources = (entry.get("provenance") or {}).get("sources", [])
        if not any(source.get("original_text") == original_create for source in sources):
            raise RuntimeError("created note did not preserve exact original input")
        if original_create in entry["body"]:
            raise RuntimeError("original-only marker leaked into cleaned body")

        original_expand = "ORIGINAL_ONLY_EXPANSION: preserve as a separate source."
        write_fixture_state(state, "expand", note_id)
        expanded = json_command(
            binary,
            config,
            env,
            ["ai", "note", "--stdin", "--note", note_id, "--output", "json"],
            original_expand,
        )
        expanded_entry = result_entry(expanded)
        expanded_sources = (expanded_entry.get("provenance") or {}).get("sources", [])
        originals = [source.get("original_text") for source in expanded_sources]
        if expanded_entry.get("id") != note_id or expanded_entry.get("version", 0) <= entry.get("version", 0):
            raise RuntimeError("expansion did not update the selected note through CAS")
        if expanded_entry.get("namespace") != "poc":
            raise RuntimeError("expansion escaped the isolated poc namespace")
        expanded_body = expanded_entry.get("body", "")
        if not expanded_body.startswith(CREATE_BODY) or EXPANSION_ADDITION not in expanded_body:
            raise RuntimeError("expansion did not preserve prior body and append new content")
        if original_expand in expanded_body:
            raise RuntimeError("expansion original input leaked into cleaned body")
        if original_create not in originals or original_expand not in originals:
            raise RuntimeError("expansion did not preserve both original source records")

        shown_output = cli_run(
            binary,
            config,
            env,
            ["note", "show", note_id, "--original", "--output", "json"],
        )
        try:
            shown = json.loads(shown_output)
        except json.JSONDecodeError as exc:
            raise RuntimeError("note show --original stdout was not valid JSON") from exc
        shown_text = json.dumps(shown, ensure_ascii=False)
        if original_create not in shown_text or original_expand not in shown_text:
            raise RuntimeError("note show --original did not expose both source inputs")

        write_fixture_state(state, "invalid")
        raw_invalid = " \nRAW_FALLBACK_INVALID: preserve these exact bytes.\t\n"
        invalid = json_command(
            binary,
            config,
            env,
            ["ai", "note", "--stdin", "--new", "--output", "json"],
            raw_invalid,
        )
        invalid_entry = result_entry(invalid)
        if invalid_entry.get("body") != raw_invalid or not invalid.get("fallback_reason"):
            raise RuntimeError("invalid provider output did not produce exact raw fallback")

        before_disabled = len(calls.read_text(encoding="utf-8").splitlines())
        stop_daemon(daemon)
        daemon = None
        write_config(config, data, wrapper, enabled=False)
        daemon = start_daemon(binary, config, env, root / "daemon-disabled.log")
        raw_disabled = " \nRAW_FALLBACK_DISABLED: preserve these exact bytes.\t\n"
        disabled = json_command(
            binary,
            config,
            env,
            ["ai", "note", "--stdin", "--new", "--output", "json"],
            raw_disabled,
        )
        disabled_entry = result_entry(disabled)
        after_disabled = len(calls.read_text(encoding="utf-8").splitlines())
        if disabled_entry.get("body") != raw_disabled or not disabled.get("fallback_reason"):
            raise RuntimeError("disabled AI did not produce exact raw fallback")
        if after_disabled != before_disabled:
            raise RuntimeError("provider wrapper launched while AI was disabled")

        stop_daemon(daemon)
        daemon = None
        write_config(config, data, wrapper, enabled=True)
        daemon = start_daemon(binary, config, env, root / "daemon-cancel.log")
        before_cancel = list_note_ids(binary, config, env)
        pid_marker = root / "blocked-provider.pid"
        write_fixture_state(state, "block", pid_marker=str(pid_marker))
        cancel_cli = subprocess.Popen(
            [
                str(binary),
                "--config",
                str(config),
                "ai",
                "note",
                "--stdin",
                "--new",
                "--output",
                "json",
            ],
            cwd=REPO,
            env=env,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        provider_pid = None
        try:
            assert cancel_cli.stdin is not None
            cancel_cli.stdin.write("CANCEL_TEST_SYNTHETIC_INPUT\n")
            cancel_cli.stdin.flush()
            cancel_cli.stdin.close()
            cancel_cli.stdin = None
            provider_pid = wait_for_provider_pid(pid_marker, cancel_cli, timeout=15)
            # Disconnect only our foreground client; cancellation must stop its provider.
            reap_cli_process(cancel_cli)
        finally:
            if cancel_cli.poll() is None:
                reap_cli_process(cancel_cli)
            if cancel_cli.stdin is not None and not cancel_cli.stdin.closed:
                cancel_cli.stdin.close()
        if provider_pid is None:
            raise RuntimeError("cancellation test did not observe a provider PID")
        wait_for_pid_exit(provider_pid, timeout=10)

        settle_deadline = time.monotonic() + 2
        while True:
            after_cancel = list_note_ids(binary, config, env)
            if after_cancel != before_cancel:
                raise RuntimeError("cancellation created a late note or raw fallback")
            if time.monotonic() >= settle_deadline:
                break
            time.sleep(0.1)

        print("PASS: isolated fake-provider create, expansion/CAS, provenance, original display, invalid-output fallback, disabled raw fallback, and disconnect cancellation")
        return 0
    except (OSError, RuntimeError, subprocess.SubprocessError, KeyError, TypeError) as exc:
        diagnostics = []
        for log in sorted(root.glob("daemon*.log")):
            try:
                text = log.read_text(encoding="utf-8", errors="replace")[-4000:]
            except OSError:
                continue
            if text:
                diagnostics.append(f"--- {log.name} (last 4000 chars) ---\n{text}")
        detail = "\n".join(diagnostics)
        print(f"FAIL: {exc}" + (f"\n{detail}" if detail else ""), file=sys.stderr)
        return 1
    finally:
        stop_daemon(daemon)
        shutil.rmtree(root, ignore_errors=True)


if __name__ == "__main__":
    raise SystemExit(main())
