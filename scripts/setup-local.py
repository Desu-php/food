#!/usr/bin/env python3
"""Prepare localhost Auth secrets without replacing existing credentials."""
import os
from pathlib import Path
import re
import secrets
import subprocess

ROOT = Path(__file__).resolve().parent.parent


def main():
    os.umask(0o077)
    path = ROOT / ".env"
    text = path.read_text() if path.exists() else (ROOT / ".env.example").read_text()
    values = dict(re.findall(r"^([A-Z][A-Z0-9_]*)=(.*)$", text, re.MULTILINE))
    if values.get("APP_ENV") != "development":
        raise SystemExit("Local setup requires APP_ENV=development")
    if values.get("COMPOSE_FILE") != "deploy/compose.yaml:deploy/compose.auth.yaml":
        raise SystemExit("Select deploy/compose.yaml:deploy/compose.auth.yaml in COMPOSE_FILE first")

    updates = {}
    for name in ("AUTH_POSTGRES_PASSWORD", "AUTH_DB_PASSWORD"):
        if not values.get(name):
            updates[name] = secrets.token_hex(32)
    updates["AUTH_SECRET_GID"] = str(os.getgid())
    for name, value in updates.items():
        pattern = rf"^{name}=.*$"
        if re.search(pattern, text, re.MULTILINE):
            text = re.sub(pattern, lambda _: f"{name}={value}", text, flags=re.MULTILINE)
        else:
            text += f"\n{name}={value}\n"
    path.write_text(text)
    path.chmod(0o600)

    # Resolve bind sources relative to the first Compose file, not the shell cwd.
    directory = (ROOT / "deploy" / values.get("AUTH_DEV_DIR", "../.local/auth")).resolve()
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    directory.chmod(0o700)
    for name in ("csrf.key", "service.key"):
        key = directory / name
        if not key.exists():
            with key.open("x") as output:
                output.write(secrets.token_hex(32) + "\n")
        key.chmod(0o640)
    key = directory / "keys.json"
    if not key.exists():
        subprocess.run(["go", "run", "./services/auth/cmd/authctl", "keygen",
                        "-out", str(key), "-kid", values.get("AUTH_ACTIVE_KID", "initial")],
                       cwd=ROOT, check=True)
    key.chmod(0o640)
    print("Local .env and Auth keys are ready; existing passwords and signing keys were preserved.")


if __name__ == "__main__":
    main()
