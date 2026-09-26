#!/usr/bin/env python3
"""Register an isolated tracked-source snapshot for real LAN Code acceptance.

Runs only through CA-verified, password-authenticated Gateway RPCs. The
password is read from a private file; the persistent files contain IDs and a
path, never cookies or credentials.
"""

import argparse
import http.cookiejar
import json
import os
from pathlib import Path
import re
import ssl
import subprocess
import sys
import urllib.error
import urllib.request


ROOT = Path(__file__).resolve().parents[2]
PRIVATE = ROOT / ".workos"
UUID7 = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")


def private_password(path: Path) -> str:
    if not path.is_absolute() or path.is_symlink():
        raise ValueError("password fixture must be an absolute, regular file")
    facts = path.stat()
    if not path.is_file() or facts.st_uid != os.getuid() or facts.st_mode & 0o077:
        raise ValueError("password fixture must be owned by this user and mode 0600 or stricter")
    secret = path.read_text(encoding="utf-8").removesuffix("\n")
    if not secret:
        raise ValueError("password fixture is empty")
    return secret


class Gateway:
    def __init__(self, origin: str, ca: Path):
        if not re.fullmatch(r"https://[0-9.]+:8443", origin):
            raise ValueError("LAN origin must be an HTTPS IPv4 address on port 8443")
        cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(ca))),
            urllib.request.HTTPCookieProcessor(cookies),
        )
        self.origin = origin
        self.cookies = cookies

    def rpc(self, service: str, method: str, body: dict | None = None) -> dict:
        path = f"/{service}/{method}"
        request = urllib.request.Request(
            self.origin + path,
            data=json.dumps(body or {}, separators=(",", ":")).encode("utf-8"),
            headers={"Content-Type": "application/json", "Origin": self.origin},
            method="POST",
        )
        try:
            with self.opener.open(request, timeout=30) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"Gateway {service}/{method} returned HTTP {error.code}") from None
        except (OSError, ValueError) as error:
            raise RuntimeError(f"Gateway {service}/{method} could not complete: {type(error).__name__}") from None

    def login(self, username: str, password: str) -> None:
        mode = self.rpc("workos.auth.v1.PasswordAuthService", "GetMode")
        if mode.get("mode") != "AUTH_MODE_PASSWORD":
            raise RuntimeError("Gateway password mode is unavailable")
        self.rpc(
            "workos.auth.v1.PasswordAuthService", "Login",
            {"username": username, "password": password,
             "deviceName": "WorkOS P0 Code setup", "deviceClass": "DEVICE_CLASS_DESKTOP"},
        )
        if not any(cookie.name == "__Host-workos_session" for cookie in self.cookies):
            raise RuntimeError("Gateway did not issue a secure owner session")

    def logout(self) -> None:
        self.rpc("workos.auth.v1.DeviceService", "Logout")


def current_commit() -> str:
    return subprocess.check_output(["git", "-C", str(ROOT), "rev-parse", "--verify", "HEAD"], text=True).strip()


def snapshot_path(commit: str) -> Path:
    workspace_root = Path(os.environ.get("WORKOS_WORKSPACE_ROOTS", str(PRIVATE / "workspaces"))).resolve()
    return workspace_root / f"workos-{commit[:12]}"


def state_path(commit: str) -> Path:
    return PRIVATE / f"lan-code-project-{commit[:12]}.json"


def write_private(path: Path, value: str) -> None:
    if path.is_symlink():
        raise ValueError(f"refusing symlink at {path}")
    temporary = path.with_name(path.name + f".tmp-{os.getpid()}")
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as output:
            output.write(value)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        if temporary.exists():
            temporary.unlink()


def prepare(gateway: Gateway, commit: str) -> None:
    subprocess.run([str(ROOT / "tools/v3-p0-native-experience/prepare-workos-workspace.sh")], check=True)
    snapshot = snapshot_path(commit)
    if snapshot.is_symlink() or not snapshot.is_dir() or (snapshot / ".workos").exists() or (snapshot / ".git").exists():
        raise RuntimeError("tracked-source snapshot is missing or includes private repository state")
    result = gateway.rpc(
        "workos.project.v1.ProjectService", "CreateProject",
        {"name": f"WorkOS P0 Code {commit[:12]}", "idempotencyKey": f"lan-p0-code-{commit[:12]}"},
    )
    project = result.get("project") or {}
    owner, project_id = project.get("ownerUserId"), project.get("id")
    if not isinstance(owner, str) or not UUID7.fullmatch(owner) or not isinstance(project_id, str) or not UUID7.fullmatch(project_id):
        raise RuntimeError("Gateway project response lacked a valid owner and project ID")
    PRIVATE.mkdir(mode=0o700, exist_ok=True)
    state = {"version": 1, "commit": commit, "ownerUserId": owner,
             "projectId": project_id, "snapshot": str(snapshot)}
    write_private(state_path(commit), json.dumps(state, sort_keys=True) + "\n")
    write_private(PRIVATE / "lan-code-workspace-mount", f"{owner}:{project_id}:{snapshot}:rw\n")
    print(f"Code test project: {project_id}")
    print(f"Tracked-source workspace: {snapshot}")
    print("Next: ./tools/lan/start.sh up, then rerun this command with 'bind'.")


def bind(gateway: Gateway, commit: str) -> None:
    state = json.loads(state_path(commit).read_text(encoding="utf-8"))
    snapshot = snapshot_path(commit)
    if state.get("version") != 1 or state.get("commit") != commit or state.get("snapshot") != str(snapshot):
        raise RuntimeError("Code project record does not match the current tracked-source snapshot")
    project_id = state.get("projectId")
    if not isinstance(project_id, str) or not UUID7.fullmatch(project_id):
        raise RuntimeError("Code project record has an invalid project ID")
    service = "workos.project.v1.ProjectWorkspaceService"
    listed = gateway.rpc(service, "ListProjectWorkspaces", {"projectId": project_id})
    existing = [item for item in listed.get("bindings", []) if item.get("state") == "WORKSPACE_BINDING_STATE_ACTIVE"]
    available = gateway.rpc(service, "ListAvailableWorkspaces", {"projectId": project_id})
    matching = [item for item in available.get("sources", []) if item.get("displayName") == snapshot.name]
    if len(matching) != 1:
        raise RuntimeError("Runtime has not registered this Code snapshot; run ./tools/lan/start.sh up first")
    source = matching[0]
    if existing:
        if len(existing) != 1 or existing[0].get("workspaceSourceId") != source.get("id"):
            raise RuntimeError("project already has a different active workspace binding")
        print(f"Code workspace already bound to project {project_id}")
        return
    gateway.rpc(service, "BindWorkspace", {
        "projectId": project_id,
        "workspaceSourceId": source["id"],
        "displayName": snapshot.name,
        "idempotencyKey": f"lan-p0-code-bind-{commit[:12]}",
    })
    print(f"Code workspace bound to project {project_id}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("prepare", "bind"))
    parser.add_argument("--username", default=os.environ.get("WORKOS_LAN_E2E_USERNAME", "owner"))
    parser.add_argument("--password-file", default=os.environ.get("WORKOS_LAN_E2E_PASSWORD_FILE", ""))
    args = parser.parse_args()
    try:
        if not args.password_file:
            raise ValueError("pass --password-file with a private fixture file path")
        secret = private_password(Path(args.password_file))
        ip = os.environ.get("WORKOS_LAN_IP", "192.168.5.5")
        gateway = Gateway(f"https://{ip}:8443", Path(os.environ.get("WORKOS_LAN_TLS_DIR", str(PRIVATE / "lan-tls"))) / "ca.crt")
        gateway.login(args.username, secret)
        try:
            commit = current_commit()
            if args.action == "prepare":
                prepare(gateway, commit)
            else:
                bind(gateway, commit)
        finally:
            gateway.logout()
        return 0
    except (OSError, RuntimeError, ValueError, subprocess.CalledProcessError, KeyError) as error:
        print(f"prepare-code-project: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
