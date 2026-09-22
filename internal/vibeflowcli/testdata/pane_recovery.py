"""Exercise real terminal keys against tmux with recording agents, without API calls."""

import json
import os
import pathlib
import pty
import select
import subprocess
import sys
import tempfile
import time

binary = str(pathlib.Path(sys.argv[1]).resolve())
conversation = "ab838172-840b-4d32-bf24-d22dd2b0fe6c"

for provider in ["claude", "codex"]:
    with tempfile.TemporaryDirectory(prefix="vf recovery '") as temporary:
        root = pathlib.Path(temporary)
        repo = root / "repo"
        repo.mkdir()
        subprocess.run(["git", "init", "-q", "-b", "main", str(repo)], check=True)
        socket = f"vf-keycheck-{os.getpid()}-{provider}"
        record = root / "args.jsonl"
        agent = root / "agent"
        hint = (f"Resume this session with:\nclaude --resume {conversation}\n" if provider == "claude"
                else f"To continue this session, run:\n  codex resume {conversation}\nOr run codex resume and select My session.\n")
        agent.write_text(f"#!{sys.executable}\n" + f"""
import json, signal, sys
with open({str(record)!r}, 'a') as f:
    f.write(json.dumps(sys.argv[1:]) + '\\n')
def stop(*_):
    print('\\n' + {hint!r}, flush=True)
    sys.exit(0)
signal.signal(signal.SIGINT, stop)
print('READY', flush=True)
while True:
    input()
    print('LIVE_ENTER', flush=True)
""")
        agent.chmod(0o755)
        (root / "config.yaml").write_text(json.dumps({
            "tmux_socket": socket,
            "default_provider": provider,
            "providers": {provider: {"binary": str(agent), "launch_template": "{{ shellQuote .Binary }}" if provider == "claude" else "{{.Binary}}"}},
        }))
        # Codex's binary token must remain unquoted to insert its subcommand;
        # the executable itself can live outside the root containing spaces.
        if provider == "codex":
            safe_agent = pathlib.Path(f"/tmp/vf-keycheck-agent-{os.getpid()}")
            safe_agent.symlink_to(agent)
            config = json.loads((root / "config.yaml").read_text())
            config["providers"][provider]["binary"] = str(safe_agent)
            (root / "config.yaml").write_text(json.dumps(config))

        def tm(*args):
            return subprocess.check_output(["tmux", "-L", socket, *args], text=True).strip()

        child = master = None
        try:
            subprocess.run([binary, "--root", str(root), "launch", "--provider", provider], cwd=repo, check=True, capture_output=True)
            session = tm("list-sessions", "-F", "#{session_name}")
            pane = tm("display-message", "-p", "-t", session, "#{pane_id}")
            tm("resize-window", "-t", session, "-x", "100", "-y", "30")
            child, master = pty.fork()
            if child == 0:
                os.environ["TERM"] = "xterm-256color"
                os.execvp("tmux", ["tmux", "-L", socket, "attach-session", "-t", session])

            def wait_for(predicate, message):
                deadline = time.monotonic() + 8
                while time.monotonic() < deadline:
                    if select.select([master], [], [], 0.03)[0]:
                        os.read(master, 65536)
                    if predicate():
                        return
                raise AssertionError(message + "\n" + tm("capture-pane", "-p", "-J", "-t", pane))

            def captures():
                return tm("capture-pane", "-p", "-J", "-t", pane, "-S", "-40")

            wait_for(lambda: "READY" in captures() and tm("list-clients", "-F", "#{client_pid}") != "", "agent/client did not start")
            client = tm("list-clients", "-F", "#{client_pid}")
            client_name = tm("list-clients", "-F", "#{client_name}")
            os.write(master, b"\r")
            wait_for(lambda: "LIVE_ENTER" in captures(), "Enter was swallowed for live agent")
            for workbench in [False, True]:
                if workbench:
                    tm("new-session", "-d", "-s", "workbench", "sleep 300")
                    sibling = tm("display-message", "-p", "-t", "workbench", "#{pane_id}")
                    tm("switch-client", "-c", client_name, "-t", "workbench")
                    tm("join-pane", "-s", pane, "-t", "workbench")
                    tm("select-pane", "-t", pane)
                before = len(record.read_text().splitlines())
                os.write(master, b"\x03")
                wait_for(lambda: tm("display-message", "-p", "-t", pane, "#{pane_dead}") == "1", "Ctrl+C did not exit agent")
                # tmux can report a dead pane before it renders the exit banner.
                wait_for(lambda: "Press Enter to resume" in captures(), "recovery hint did not appear")
                exited_output = captures()
                os.write(master, b"\r")
                wait_for(lambda: len(record.read_text().splitlines()) > before, "Enter did not resume agent")
                args = json.loads(record.read_text().splitlines()[-1])
                assert conversation in args, (args, exited_output)
                assert tm("display-message", "-p", "-t", pane, "#{pane_dead}") == "0"
                assert tm("list-clients", "-F", "#{client_pid}") == client, "client detached"
                if workbench:
                    assert tm("display-message", "-p", "-t", sibling, "#{pane_dead}") == "0", "sibling stopped"
                print(f"PASS {provider}: Ctrl+C -> Enter, same pane and client, workbench={workbench}", flush=True)

            # The TUI can reuse a VibeFlow session ID with a different provider
            # while the old pane remains. Never launch that provider in this pane.
            os.write(master, b"\x03")
            wait_for(lambda: tm("display-message", "-p", "-t", pane, "#{pane_dead}") == "1", "agent did not exit")
            config = json.loads((root / "config.yaml").read_text())
            other = "cursor"
            config["providers"][other] = {"binary": str(agent), "launch_template": "{{ shellQuote .Binary }}"}
            (root / "config.yaml").write_text(json.dumps(config))
            entries = json.loads((root / "sessions.json").read_text())
            entries[0]["provider"] = other
            entries[0]["tmux_session"] = "vibeflow_" + other + "-" + entries[0]["name"]
            (root / "sessions.json").write_text(json.dumps(entries))
            result = subprocess.run([binary, "--root", str(root), "resume-pane", pane], text=True, capture_output=True)
            assert result.returncode != 0, "recovery accepted another provider's session metadata"
            assert tm("display-message", "-p", "-t", pane, "#{pane_dead}") == "1", "reassigned metadata replaced the old pane"
            print(f"PASS {provider}: reassigned session identity preserves the dead pane", flush=True)
        finally:
            subprocess.run(["tmux", "-L", socket, "kill-server"], capture_output=True)
            if master is not None:
                os.close(master)
            if child:
                os.waitpid(child, 0)
            if provider == "codex":
                safe_agent.unlink(missing_ok=True)
