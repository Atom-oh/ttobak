"""Build-time pin verifier for the production whisper image (ADR-035).

Fails the docker build if any exact-pinned package didn't survive pip's
dependency resolution -- the guard that makes "pins must not move" a loud
invariant instead of a comment. Keep this list in lockstep with the
Dockerfile's pip install pins.

Deliberately raises SystemExit rather than using bare asserts: a build
gate must not be silently disabled by PYTHONOPTIMIZE/-O ever reaching the
invocation."""
import importlib.metadata as md

import torch


def fail(msg: str) -> None:
    raise SystemExit(f"PIN VERIFICATION FAILED: {msg}")


# Version AND CUDA variant: a floating transitive dep could replace the
# cu128 wheel with a same-version +cpu / default-PyPI wheel, which would
# only surface at GPU-task runtime as a swallowed _diarize failure
# (silent unlabeled fallback) -- exactly what this gate exists to prevent.
if torch.__version__ != "2.8.0+cu128":
    fail(f"torch moved: {torch.__version__!r} (want exactly '2.8.0+cu128' -- "
         f"version AND local wheel label)")
if torch.version.cuda != "12.8":
    fail(f"torch CUDA variant moved: cuda={torch.version.cuda!r} "
         f"(want 12.8 / the cu128 wheel)")

PINS = (
    ("torchaudio", "2.8.0+cu128"),
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
    if got != want:
        fail(f"{pkg} moved: {got} != {want}")
print("pins verified (incl. torch cu128 variant)")
