#!/usr/bin/env python3
"""rex config - configure Telegram bot and manage the daemon."""

import json
import os
import plistlib
import subprocess
import sys
from pathlib import Path

INSTALL_DIR = Path(__file__).parent
BASE_DIR = Path(os.environ["REX_PROJECT_DIR"]) if "REX_PROJECT_DIR" in os.environ else INSTALL_DIR
CONFIG_PATH = BASE_DIR / "config.json"
PLIST_NAME = "com.rex.agent-runner"
PLIST_PATH = Path.home() / "Library" / "LaunchAgents" / f"{PLIST_NAME}.plist"
LOG_DIR = BASE_DIR / "logs"
VENV_PYTHON = str(INSTALL_DIR / "venv" / "bin" / "python")
BOT_SCRIPT = str(INSTALL_DIR / "rex_bot.py")


def load_config() -> dict:
    if CONFIG_PATH.exists():
        with open(CONFIG_PATH) as f:
            return json.load(f)
    return {}


def save_config(cfg: dict) -> None:
    with open(CONFIG_PATH, "w") as f:
        json.dump(cfg, f, indent=2)
        f.write("\n")


def cmd_setup() -> None:
    """Interactive setup for config.json."""
    cfg = load_config()

    print("Rex Configuration Setup")
    print("=" * 40)

    # Telegram bot token
    current_token = cfg.get("telegram_bot_token", "")
    masked = current_token[:8] + "..." if current_token else "(not set)"
    token = input(f"Telegram bot token [{masked}]: ").strip()
    if token:
        cfg["telegram_bot_token"] = token

    # Allowed user IDs
    current_ids = cfg.get("allowed_user_ids", [])
    ids_str = ", ".join(str(i) for i in current_ids) if current_ids else "(not set)"
    ids_input = input(f"Allowed Telegram user IDs (comma-separated) [{ids_str}]: ").strip()
    if ids_input:
        cfg["allowed_user_ids"] = [int(i.strip()) for i in ids_input.split(",")]

    # Chat ID for notifications
    current_chat = cfg.get("telegram_chat_id", "")
    default_chat = current_chat or (cfg.get("allowed_user_ids", [None])[0] if cfg.get("allowed_user_ids") else "")
    chat_input = input(f"Telegram chat ID for notifications [{default_chat or '(not set)'}]: ").strip()
    if chat_input:
        cfg["telegram_chat_id"] = int(chat_input)
    elif not current_chat and default_chat:
        cfg["telegram_chat_id"] = int(default_chat)

    # Job port
    current_port = cfg.get("job_port", 9821)
    port_input = input(f"Job HTTP port [{current_port}]: ").strip()
    if port_input:
        cfg["job_port"] = int(port_input)
    elif "job_port" not in cfg:
        cfg["job_port"] = 9821

    # Workspace
    current_ws = cfg.get("workspace", "./workspace")
    ws_input = input(f"Workspace directory [{current_ws}]: ").strip()
    if ws_input:
        cfg["workspace"] = ws_input
    elif "workspace" not in cfg:
        cfg["workspace"] = "./workspace"

    save_config(cfg)
    print(f"\nConfig saved to {CONFIG_PATH}")


def cmd_show() -> None:
    """Show current configuration."""
    cfg = load_config()
    if not cfg:
        print("No config found. Run: rex config setup")
        return

    token = cfg.get("telegram_bot_token", "")
    masked = token[:8] + "..." + token[-4:] if len(token) > 12 else "(not set)"

    print(f"  telegram_bot_token:  {masked}")
    print(f"  allowed_user_ids:    {cfg.get('allowed_user_ids', [])}")
    print(f"  telegram_chat_id:    {cfg.get('telegram_chat_id', '(default)')}")
    print(f"  job_port:            {cfg.get('job_port', 9821)}")
    print(f"  workspace:           {cfg.get('workspace', './workspace')}")
    print(f"  config file:         {CONFIG_PATH}")


def _build_plist() -> dict:
    """Build the launchd plist dictionary."""
    LOG_DIR.mkdir(exist_ok=True)
    return {
        "Label": PLIST_NAME,
        "ProgramArguments": [VENV_PYTHON, BOT_SCRIPT],
        "WorkingDirectory": str(BASE_DIR),
        "RunAtLoad": True,
        "KeepAlive": True,
        "StandardOutPath": str(LOG_DIR / "daemon.log"),
        "StandardErrorPath": str(LOG_DIR / "daemon.err.log"),
        "EnvironmentVariables": {
            "PATH": os.environ.get("PATH", "/usr/local/bin:/usr/bin:/bin"),
            "REX_PROJECT_DIR": str(BASE_DIR),
        },
    }


def cmd_start() -> None:
    """Install and start the launchd daemon."""
    if not CONFIG_PATH.exists():
        print("No config found. Run: rex config setup", file=sys.stderr)
        sys.exit(1)

    LOG_DIR.mkdir(exist_ok=True)
    PLIST_PATH.parent.mkdir(parents=True, exist_ok=True)

    plist = _build_plist()
    with open(PLIST_PATH, "wb") as f:
        plistlib.dump(plist, f)

    # Unload first if already loaded
    subprocess.run(
        ["launchctl", "unload", str(PLIST_PATH)],
        capture_output=True,
    )
    result = subprocess.run(
        ["launchctl", "load", str(PLIST_PATH)],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        print(f"Failed to start daemon: {result.stderr}", file=sys.stderr)
        sys.exit(1)

    print(f"Rex daemon started.")
    print(f"  Logs: {LOG_DIR}/daemon.log")


def cmd_stop() -> None:
    """Stop and unload the launchd daemon."""
    if not PLIST_PATH.exists():
        print("Daemon is not installed.")
        return

    result = subprocess.run(
        ["launchctl", "unload", str(PLIST_PATH)],
        capture_output=True,
        text=True,
    )
    PLIST_PATH.unlink(missing_ok=True)

    if result.returncode != 0 and "Could not find" not in result.stderr:
        print(f"Failed to stop daemon: {result.stderr}", file=sys.stderr)
        sys.exit(1)

    print("Rex daemon stopped.")


def cmd_status() -> None:
    """Check if the daemon is running."""
    result = subprocess.run(
        ["launchctl", "list", PLIST_NAME],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        print("Rex daemon: not running")
        return

    cfg = load_config()
    port = cfg.get("job_port", 9821)
    dashboard_url = f"http://127.0.0.1:{port}/"

    # Parse PID from launchctl output
    for line in result.stdout.splitlines():
        if '"PID"' in line:
            pid = line.split("=")[-1].strip().rstrip(";")
            print(f"Rex daemon: running (PID {pid})")
            print(f"Dashboard:  {dashboard_url}")
            return

    print("Rex daemon: loaded (not currently running)")
    print(f"Dashboard:  {dashboard_url}")


def cmd_logs(follow: bool = False) -> None:
    """Show all logs (daemon, errors, jobs).

    The main log is ``rex.log`` which rotates hourly.  When following we
    only tail the current (active) log files so the output stays useful.
    """
    if not LOG_DIR.exists():
        print("No logs yet.")
        return

    if follow:
        # Follow only the active (non-rotated) logs
        active = sorted(LOG_DIR.glob("*.log")) + sorted(LOG_DIR.glob("*.err.log"))
        active = [f for f in active if f.suffix == ".log" or f.name.endswith(".err.log")]
        if not active:
            print("No logs yet.")
            return
        subprocess.run(["tail", "-f"] + [str(f) for f in active])
    else:
        # Show recent entries from all logs including rotated ones
        log_files = sorted(LOG_DIR.glob("rex.log*")) + sorted(LOG_DIR.glob("daemon.*")) + sorted(LOG_DIR.glob("callback_*"))
        if not log_files:
            print("No logs yet.")
            return
        subprocess.run(["tail", "-50"] + [str(f) for f in log_files])


def usage() -> None:
    print("""Usage: rex config <command>

Commands:
  setup          Interactive configuration
  show           Show current config
  start          Start the bot daemon
  stop           Stop the bot daemon
  status         Check daemon status
  logs           Show recent daemon logs
  logs -f        Follow daemon logs""")


def main() -> None:
    args = sys.argv[1:]

    if not args:
        cmd_show()
        print()
        cmd_status()
        return

    cmd = args[0]

    if cmd == "setup":
        cmd_setup()
    elif cmd == "show":
        cmd_show()
    elif cmd == "start":
        cmd_start()
    elif cmd == "stop":
        cmd_stop()
    elif cmd == "status":
        cmd_status()
    elif cmd == "logs":
        follow = "-f" in args
        cmd_logs(follow=follow)
    elif cmd in ("help", "-h", "--help"):
        usage()
    else:
        print(f"Unknown config command: {cmd}")
        usage()
        sys.exit(1)


if __name__ == "__main__":
    main()
