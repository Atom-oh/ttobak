"""Build-time pin verifier for the production whisper image (ADR-035).

Fails the docker build if any exact-pinned package didn't survive pip's
dependency resolution -- the guard that makes "pins must not move" a loud
invariant instead of a comment. Keep this list in lockstep with the
Dockerfile's pip install pins."""
import importlib.metadata as md

import torch

assert torch.__version__.startswith("2.8.0"), f"torch moved: {torch.__version__}"
PINS = (
    ("faster-whisper", "1.2.1"),
    ("ctranslate2", "4.8.1"),
    ("onnxruntime", "1.29.0"),
    ("av", "18.1.0"),
    ("tokenizers", "0.23.1"),
    ("huggingface-hub", "1.29.0"),
    ("torchcodec", "0.7.0"),
    ("pyannote.audio", "4.0.7"),
)
for pkg, want in PINS:
    got = md.version(pkg)
    assert got == want, f"{pkg} moved: {got} != {want}"
print("pins verified")
