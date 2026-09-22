#!/usr/bin/env python3
"""Fixture-only shared desktop durability check; never prints user content."""
import json
import os
import pathlib
import sys
import urllib.request

urllib.request.install_opener(urllib.request.build_opener(urllib.request.ProxyHandler({})))
origin = "http://127.0.0.1:" + os.environ["WORKOS_V2_GATEWAY_PORT"]
request = urllib.request.Request(
    origin + "/workos.desktop.v1.DesktopService/GetDesktop",
    data=b"{}",
    headers={"Content-Type": "application/json"},
)
with urllib.request.urlopen(request, timeout=15) as response:
    state = json.load(response)["state"]
path = pathlib.Path(os.environ["WORKOS_V2_DIR"]) / "shared-desktop-restart.json"
if sys.argv[1] == "seed":
    assert state.get("revision"), "desktop was not initialized"
    path.write_text(json.dumps(state, sort_keys=True))
else:
    assert state == json.loads(path.read_text()), "desktop changed across Core/Gateway restart"
    print("Shared desktop Core/Gateway restart durability: PASS")
