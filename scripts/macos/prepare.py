#!/usr/bin/env python3
"""Prepare a user launchd installation from source without registering services, starting them, or modifying Codex files."""
import argparse
import json
import ipaddress
import os
from pathlib import Path
import platform
import plistlib
import secrets
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--port", type=int, default=7435)
    parser.add_argument("--bind", default="127.0.0.1", help="Public gateway bind address: loopback or Tailscale IPv4")
    args = parser.parse_args()
    try:
        address = ipaddress.IPv4Address(args.bind)
    except ipaddress.AddressValueError:
        parser.error("FILO_INSTALL_BIND: An IPv4 address is required")
    if str(address) != "127.0.0.1" and address not in ipaddress.IPv4Network("100.64.0.0/10"):
        parser.error("FILO_INSTALL_BIND: Only loopback or Tailscale IPv4 is allowed")
    if platform.system() != "Darwin" or os.geteuid() == 0:
        parser.error("FILO_INSTALL_PLATFORM: Run as a non-root macOS user")
    if not 1024 <= args.port <= 65533:
        parser.error("FILO_INSTALL_PORT: Choose three consecutive free ports with a base port in 1024..65533")
    root = args.directory.expanduser().absolute()
    # Create exclusively; never overwrite an existing installation or credentials.
    os.umask(0o077)
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    (root / "bin").mkdir()
    (root / "logs").mkdir()
    gateway, worker = root / "gateway", root / "worker"
    gateway.mkdir()
    worker.mkdir()
    source = Path(__file__).resolve().parents[2]
    binary = root / "bin/filo"
    subprocess.run(["go", "build", "-o", str(binary), "./cmd/filo"], cwd=source, check=True)
    uid = str(os.geteuid())
    native_url = f"ws://127.0.0.1:{args.port + 2}"
    for directory in (gateway, worker):
        (directory / "token").write_text(secrets.token_hex(32))
    (gateway / "config.json").write_text(json.dumps({
        "bind": args.bind, "port": args.port, "desktopSid": uid,
        "workerUrl": f"http://127.0.0.1:{args.port + 1}",
        "workerTokenPath": str(worker / "token"), "sources": ["codex-desktop"]}))
    (worker / "config.json").write_text(json.dumps({
        "bind": "127.0.0.1", "port": args.port + 1, "desktopSid": uid,
        "appServerUrl": native_url, "sources": ["codex-desktop"]}))
    for role, command in {
        "host": ["macos-native-host", native_url],
        "worker": ["standalone", str(worker)],
        "gateway": ["system-service", str(gateway)],
    }.items():
        label = f"dev.filo.{role}"
        record = {
            "Label": label, "ProgramArguments": [str(binary), *command],
            "WorkingDirectory": str(root), "RunAtLoad": True,
            "KeepAlive": {"SuccessfulExit": False}, "ThrottleInterval": 15,
            "ProcessType": "Background", "ExitTimeOut": 30,
            "StandardOutPath": str(root / f"logs/{role}.out.log"),
            "StandardErrorPath": str(root / f"logs/{role}.err.log"),
        }
        with (root / f"{label}.plist").open("wb") as stream:
            plistlib.dump(record, stream)
    print(f"Prepared {root}; services have not been registered or started. See docs/macos.md.")


if __name__ == "__main__":
    main()
