"""Indexer-owned, offline CPU child for index/v1/embedding.proto JSON lines."""

import hashlib
import importlib.metadata
import json
import pathlib
import sys

import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer

ort.disable_telemetry_events()


def load(directory):
    recipe = pathlib.Path(__file__).with_name("model.json").read_bytes()
    config = json.loads(recipe)
    for package in ("numpy", "onnxruntime", "tokenizers"):
        if importlib.metadata.version(package) != config["runtime"][package]:
            raise ValueError("runtime version differs")

    def verified(name):
        with (directory / name).open("rb") as file:
            data = file.read((512 << 20) + 1)
        if len(data) > 512 << 20 or hashlib.sha256(data).hexdigest() != config["files"][name]:
            raise ValueError("model checksum differs")
        return data

    # The checked bytes are the loaded bytes; neither loader reopens a mutable path.
    tokenizer = Tokenizer.from_str(verified("tokenizer.json").decode("utf8"))
    tokenizer.enable_truncation(max_length=config["max_tokens"])
    options = ort.SessionOptions()
    options.intra_op_num_threads = options.inter_op_num_threads = config["runtime"]["threads"]
    options.execution_mode = ort.ExecutionMode.ORT_SEQUENTIAL
    options.add_session_config_entry("session.intra_op.allow_spinning", "0")
    options.log_severity_level = 4
    model = ort.InferenceSession(
        verified("model.onnx"), sess_options=options, providers=[config["runtime"]["provider"]]
    )
    return config, "sha256:" + hashlib.sha256(recipe).hexdigest(), tokenizer, model


def unique_object(pairs):
    value = dict(pairs)
    if len(value) != len(pairs):
        raise ValueError("duplicate field")
    return value


def run(directory):
    config, fingerprint, tokenizer, model = load(directory)
    for line in iter(lambda: sys.stdin.buffer.readline(65537), b""):
        if len(line) > 65536 or not line.endswith(b"\n"):
            raise ValueError("invalid frame")
        request = json.loads(line, object_pairs_hook=unique_object)
        if not isinstance(request, dict) or set(request) != {"inputKind", "text"}:
            raise ValueError("invalid fields")
        kind, text = request["inputKind"], request["text"]
        if isinstance(kind, int) and not isinstance(kind, bool):
            kind = {1: "LOCAL_EMBEDDING_INPUT_KIND_QUERY", 2: "LOCAL_EMBEDDING_INPUT_KIND_DOCUMENT"}.get(kind)
        if not isinstance(kind, str) or kind not in config["prefixes"]:
            raise ValueError("invalid kind")
        if not isinstance(text, str) or not text or len(text.encode("utf8")) > config["max_input_bytes"]:
            raise ValueError("invalid text")
        encoded = tokenizer.encode(config["prefixes"][kind] + text)
        inputs = {
            "input_ids": np.array([encoded.ids], dtype=np.int64),
            "attention_mask": np.array([encoded.attention_mask], dtype=np.int64),
            "token_type_ids": np.array([encoded.type_ids], dtype=np.int64),
        }
        hidden = model.run(None, {item.name: inputs[item.name] for item in model.get_inputs()})[0]
        mask = inputs["attention_mask"][..., None]
        vector = (hidden * mask).sum(1) / mask.sum(1)
        vector = (vector / np.linalg.norm(vector, axis=1, keepdims=True))[0].astype(np.float32)
        if vector.shape != (config["dimensions"],) or not np.isfinite(vector).all():
            raise ValueError("invalid output")
        result = json.dumps({"vector": vector.tolist(), "modelFingerprint": fingerprint}, allow_nan=False)
        if len(result) + 1 > 32768:
            raise ValueError("output frame exceeded")
        print(result, flush=True)


if __name__ == "__main__":
    try:
        if len(sys.argv) != 2:
            raise ValueError("model directory required")
        run(pathlib.Path(sys.argv[1]))
    except Exception:
        # Inputs, paths and third-party diagnostics never become application logs.
        print("local embedding unavailable", file=sys.stderr)
        sys.exit(1)
