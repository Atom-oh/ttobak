"""Bounded, versioned parser result contract."""
import json
from dataclasses import asdict, dataclass, fields


@dataclass(frozen=True)
class Limits:
    max_input_bytes: int = 20 * 1024 * 1024
    max_zip_bytes: int = 64 * 1024 * 1024
    max_entry_bytes: int = 16 * 1024 * 1024
    max_entries: int = 2048
    max_zip_ratio: int = 200
    max_xml_bytes: int = 8 * 1024 * 1024
    max_xml_nodes: int = 200000
    max_xml_depth: int = 64
    max_pages: int = 200
    max_slides: int = 200
    max_units: int = 4000
    max_text_bytes: int = 256 * 1024
    max_result_bytes: int = 1024 * 1024
    max_pdf_stream_bytes: int = 8 * 1024 * 1024
    max_pdf_objects: int = 50000
    memory_bytes: int = 512 * 1024 * 1024
    cpu_seconds: int = 8
    wall_seconds: float = 12.0
    max_stderr_bytes: int = 16384

    def validate(self):
        defaults = Limits()
        for field in fields(self):
            value, ceiling = getattr(self, field.name), getattr(defaults, field.name)
            valid_type = type(value) in (int, float) if field.name == "wall_seconds" else type(value) is int
            if not valid_type or not 0 < value <= ceiling:
                raise ParseFailure("INVALID_OPTIONS")
        # Even failure envelopes must fit the selected protocol bound.
        if self.max_result_bytes < 1024:
            raise ParseFailure("INVALID_OPTIONS")
        return self

    def to_dict(self):
        return asdict(self)


MESSAGES = {
    "INVALID_OPTIONS": "Invalid parser limits or format.",
    "UNSUPPORTED_FORMAT": "Only PDF, PPTX, DOCX, and UTF-8 Markdown are supported.",
    "CORRUPT_DOCUMENT": "The document is corrupt or has an inconsistent structure.",
    "UNSAFE_DOCUMENT": "Active content, unsafe external relationships, or unsafe package/XML features are not allowed.",
    "ENCRYPTED_DOCUMENT": "Encrypted documents are not supported; supply a decrypted copy.",
    "ENCRYPTED_OR_LEGACY_OFFICE": "Encrypted or legacy OLE Office containers are not supported.",
    "INVALID_ENCODING": "Markdown must be valid UTF-8.",
    "NO_EXTRACTABLE_TEXT": "No supported embedded text was found.",
    "OCR_REQUIRED": "Image-only PDF has no extractable text; OCR is required.",
    "LIMIT_EXCEEDED": "A document size, structure, or extracted-output limit was exceeded.",
    "PDF_PARSE_WARNING": "PDF text decoding was incomplete or required a parser recovery.",
    "RESOURCE_LIMIT": "The parser exceeded its memory or CPU allowance.",
    "TIMEOUT": "The parser exceeded its wall-time allowance.",
    "WORKER_FAILED": "The isolated parser did not return a valid result.",
    "WORKER_OUTPUT_LIMIT": "The parser exceeded its stdout or stderr allowance.",
    "ISOLATION_UNAVAILABLE": "Required process resource controls are unavailable.",
    "INPUT_UNAVAILABLE": "Input must be a readable regular file within the size limit.",
}


class ParseFailure(Exception):
    def __init__(self, code):
        self.code = code
        super().__init__(MESSAGES[code])


def encoded(result):
    return (json.dumps(result, ensure_ascii=False, separators=(",", ":"), allow_nan=False) + "\n").encode("utf-8")


def failure(fmt, code):
    return {
        "schemaVersion": 1, "format": fmt if fmt in ("pdf", "pptx", "docx", "md") else "unknown",
        "status": "failed", "complete": False, "scope": "none", "units": [], "warnings": [],
        "error": {"code": code, "message": MESSAGES[code]},
        "metrics": {"units": 0, "textBytes": 0},
    }


class Collector:
    def __init__(self, fmt, limits, scope):
        self.limits = limits
        self.result = {
            "schemaVersion": 1, "format": fmt, "status": "succeeded", "complete": True,
            "scope": scope, "units": [], "warnings": [], "error": None,
            "metrics": {"units": 0, "textBytes": 0},
        }

    def check_size(self):
        if len(encoded(self.result)) > self.limits.max_result_bytes:
            raise ParseFailure("LIMIT_EXCEEDED")

    def add(self, text, location):
        text = text.strip()
        if not text:
            return False
        size = len(text.encode("utf-8", "strict"))
        metrics = self.result["metrics"]
        if metrics["units"] >= self.limits.max_units or metrics["textBytes"] + size > self.limits.max_text_bytes:
            raise ParseFailure("LIMIT_EXCEEDED")
        self.result["units"].append({"text": text, "location": location})
        metrics["units"] += 1
        metrics["textBytes"] += size
        self.check_size()
        return True

    def warn(self, code, location):
        if len(self.result["warnings"]) >= 400:
            raise ParseFailure("LIMIT_EXCEEDED")
        self.result["warnings"].append({"code": code, "location": location})
        self.result["complete"] = False
        self.result["status"] = "partial"
        self.check_size()

    def finish(self, empty_code="NO_EXTRACTABLE_TEXT"):
        if not self.result["units"]:
            raise ParseFailure(empty_code)
        self.check_size()
        return self.result
