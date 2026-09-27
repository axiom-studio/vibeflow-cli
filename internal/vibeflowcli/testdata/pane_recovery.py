"""Exercise real terminal keys against tmux with recording agents, without API calls."""

import fcntl
import json
import os
import pathlib
import pty
import select
import shlex
import struct
import shutil
import subprocess
import sys
import tempfile
import termios
import time

binary = str(pathlib.Path(sys.argv[1]).resolve())
test_binary = str(pathlib.Path(sys.argv[2]).resolve())
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

            recent_client = bytearray()
            terminal_output = bytearray()
            screen_width, screen_height = 100, 30

            def visible_screen():
                env = dict(os.environ, VIBEFLOW_PANE_RECOVERY_HELPER="screen",
                           VIBEFLOW_PANE_RECOVERY_WIDTH=str(screen_width),
                           VIBEFLOW_PANE_RECOVERY_HEIGHT=str(screen_height))
                return subprocess.run([test_binary, "-test.run=^TestPaneRecoveryHelperProcess$"],
                                      env=env, input=bytes(terminal_output), capture_output=True,
                                      check=True).stdout.decode(errors="replace")

            def wait_for(predicate, message):
                deadline = time.monotonic() + 8
                while time.monotonic() < deadline:
                    if select.select([master], [], [], 0.03)[0]:
                        chunk = os.read(master, 65536)
                        recent_client.extend(chunk)
                        terminal_output.extend(chunk)
                        del recent_client[:-8192]
                    if predicate():
                        return
                version = subprocess.check_output(["tmux", "-V"], text=True).strip()
                state = tm("display-message", "-p", "-t", pane,
                           "dead=#{pane_dead} status=#{pane_dead_status} signal=#{pane_dead_signal} "
                           "time=#{pane_dead_time} size=#{pane_width}x#{pane_height}")
                extra = ""
                if message == "recovery hint did not appear":
                    pane_pid = tm("display-message", "-p", "-t", pane, "#{pane_pid}")
                    process = subprocess.run(["ps", "-o", "stat=,comm=", "-p", pane_pid],
                                             capture_output=True, text=True) if pane_pid.isdecimal() else None
                    fmt = tm("show-options", "-p", "-v", "-t", pane, "remain-on-exit-format")
                    extra = (f" server={tm('display-message', '-p', '-t', pane, '#{version}')}"
                             f" tmux_bin={shutil.which('tmux')}"
                             f" remain={tm('show-options', '-p', '-v', '-t', pane, 'remain-on-exit')}"
                             f" format_has_hint={'Press Enter to resume' in fmt}"
                             f" pane_pid={pane_pid} pane_process={process.stdout.strip() if process and process.returncode == 0 else 'gone'}"
                             f" attached_hint_emitted={b'Press Enter to resume' in recent_client}")
                raise AssertionError(f"{message} ({version}, {provider}, {state}{extra})\n" + captures())

            def captures():
                return tm("capture-pane", "-p", "-J", "-t", pane, "-S", "-40")

            def resume_with_enter(before):
                command = tm("show-options", "-p", "-v", "-t", pane, "@vibeflow_resume")
                marker = f"vf-resume-{os.getpid()}-{provider}-{before}"
                result = root / f"resume-{before}.status"
                script = (f'{command} "$1"; status=$?; '
                          f'printf "%s" "$status" > {shlex.quote(str(result))}; '
                          f'tmux -L {shlex.quote(socket)} wait-for -S {shlex.quote(marker)}; '
                          'exit "$status"')
                tm("set-option", "-p", "-t", pane, "@vibeflow_resume",
                   "sh -c " + shlex.quote(script) + " --")
                waiter = subprocess.Popen(["tmux", "-L", socket, "wait-for", marker],
                                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                try:
                    os.write(master, b"\r")
                    wait_for(lambda: len(record.read_text().splitlines()) > before,
                             "Enter did not resume agent")
                    assert waiter.wait(timeout=8) == 0, "resume command did not complete"
                    assert result.read_text() == "0", "resume command failed"
                finally:
                    if waiter.poll() is None:
                        waiter.kill()
                        waiter.wait()

            wait_for(lambda: "READY" in captures() and tm("list-clients", "-F", "#{client_pid}") != "", "agent/client did not start")
            client = tm("list-clients", "-F", "#{client_pid}")
            client_name = tm("list-clients", "-F", "#{client_name}")
            os.write(master, b"\r")
            wait_for(lambda: "LIVE_ENTER" in captures(), "Enter was swallowed for live agent")
            for workbench in [False, True]:
                if workbench:
                    tm("set-option", "-g", "detach-on-destroy", "off")
                    sibling_session = f"vibeflow_{provider}-sibling"
                    tm("new-session", "-d", "-s", sibling_session, "sleep 300")
                    sibling = tm("display-message", "-p", "-t", sibling_session, "#{pane_id}")
                    env = dict(os.environ, VIBEFLOW_PANE_RECOVERY_HELPER="compose",
                               VIBEFLOW_PANE_RECOVERY_SOCKET=socket,
                               VIBEFLOW_PANE_RECOVERY_SESSIONS=f"{session},{sibling_session}")
                    subprocess.run([test_binary, "-test.run=^TestPaneRecoveryHelperProcess$"],
                                   env=env, check=True, capture_output=True)
                    tm("switch-client", "-c", client_name, "-t", "vibeflow_workbench")
                    tm("select-pane", "-t", pane)
                before = len(record.read_text().splitlines())
                recent_client.clear()
                wait_for(lambda: "Press Enter to resume" not in visible_screen(),
                         "live pane retained recovery hint")
                assert "Press Enter to resume" not in tm("display-message", "-p", "-t", pane,
                                                          "-F", "#{E:status-left}"), "live pane advertised recovery"
                # Exercise the fallback without relying on tmux's native exit banner.
                subprocess.run(["tmux", "-L", socket, "set-option", "-p", "-t", pane,
                                "remain-on-exit-format", ""], capture_output=True)
                exited_pid = tm("display-message", "-p", "-t", pane, "#{pane_pid}")
                os.write(master, b"\x03")
                wait_for(lambda: tm("display-message", "-p", "-t", pane, "#{pane_dead}") == "1", "Ctrl+C did not exit agent")
                wait_for(lambda: "Press Enter to resume" in visible_screen(), "recovery hint did not appear")
                if workbench:
                    tm("select-pane", "-t", sibling)
                    assert tm("display-message", "-p", "-t", sibling,
                              "#{@vibeflow_resume}") == "", "sibling acquired recovery identity"
                    wait_for(lambda: "Ctrl-t" in visible_screen() and
                             "Press Enter to resume" not in visible_screen(),
                             "live sibling showed recovery hint")
                    tm("select-pane", "-t", pane)
                    fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack("HHHH", 12, 40, 0, 0))
                    wait_for(lambda: tm("display-message", "-p", "-t", pane,
                                        "#{window_width}") == "40", "workbench did not narrow")
                    screen_width, screen_height = 40, 12
                    terminal_output.clear()
                    tm("refresh-client", "-t", client_name)
                    wait_for(lambda: "Press Enter to resume" in visible_screen(),
                             "narrow workbench lost recovery hint")
                    fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 100, 0, 0))
                    wait_for(lambda: tm("display-message", "-p", "-t", pane,
                                        "#{window_width}") == "100", "workbench did not widen")
                    screen_width, screen_height = 100, 30
                    terminal_output.clear()
                    tm("refresh-client", "-t", client_name)
                exited_output = captures()
                resume_with_enter(before)
                args = json.loads(record.read_text().splitlines()[-1])
                assert conversation in args, (args, exited_output)
                assert tm("display-message", "-p", "-t", pane, "#{pane_dead}") == "0"
                wait_for(lambda: subprocess.run(["ps", "-p", exited_pid, "-o", "stat="],
                                               capture_output=True).returncode != 0,
                         "resumed pane left its old process unreaped")
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
