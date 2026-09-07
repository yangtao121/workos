"""Download only the pinned public model files; inference never calls this tool."""

import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import urllib.request


def valid(path, checksum):
    if path.is_symlink():
        return False
    with path.open("rb") as file:
        return hashlib.file_digest(file, "sha256").hexdigest() == checksum


def fetch(directory):
    recipe = Path(__file__).resolve().parents[2] / "internal/indexer/adapters/localembedding/model.json"
    config = json.loads(recipe.read_bytes())
    directory.mkdir(parents=True, exist_ok=True)
    for name, checksum in config["files"].items():
        target = directory / name
        if target.exists() or target.is_symlink():
            if not valid(target, checksum):
                raise ValueError("existing model cache failed checksum validation")
            continue
        url = f"https://huggingface.co/{config['model']}/resolve/{config['revision']}/onnx/{name}"
        temporary = None
        try:
            with tempfile.NamedTemporaryFile(dir=directory, delete=False) as file:
                temporary = Path(file.name)
                size = 0
                with urllib.request.urlopen(url, timeout=60) as response:
                    while chunk := response.read(1 << 20):
                        size += len(chunk)
                        if size > 512 << 20:
                            raise ValueError("model download exceeded its bound")
                        file.write(chunk)
                file.flush()
                os.fsync(file.fileno())
            if not valid(temporary, checksum):
                raise ValueError("downloaded model checksum differs")
            temporary.chmod(0o444)
            try:
                os.link(temporary, target)
            except FileExistsError:
                if not valid(target, checksum):
                    raise ValueError("concurrent model cache failed validation")
        finally:
            if temporary is not None:
                temporary.unlink(missing_ok=True)
    print("Pinned model checksums verified")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("model cache directory required")
    fetch(Path(sys.argv[1]).absolute())
