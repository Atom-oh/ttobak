"""Pure in-memory parsing. Use worker.extract_file for untrusted inputs."""
import io
import logging

from contract import Collector, Limits, ParseFailure, failure


def extract_bytes(data, fmt, limits=None):
    limits = limits or Limits()
    try:
        limits.validate()
        if fmt not in ("pdf", "pptx", "docx", "md"):
            raise ParseFailure("UNSUPPORTED_FORMAT")
        if not isinstance(data, bytes):
            raise ParseFailure("INVALID_OPTIONS")
        if len(data) > limits.max_input_bytes:
            raise ParseFailure("LIMIT_EXCEEDED")
        if fmt == "md":
            return markdown(data, limits)
        if fmt == "pdf":
            return pdf_text(data, limits)
        from ooxml import office_text
        return office_text(data, fmt, limits)
    except ParseFailure as error:
        return failure(fmt, error.code)
    except MemoryError:
        return failure(fmt, "RESOURCE_LIMIT")
    except Exception:
        # No source text, path, library traceback, or arbitrary exception string
        # is reflected in a result that will eventually reach a user.
        return failure(fmt, "CORRUPT_DOCUMENT")


def markdown(data, limits):
    try:
        text = data.decode("utf-8-sig", "strict")
    except UnicodeDecodeError:
        raise ParseFailure("INVALID_ENCODING") from None
    output = Collector("md", limits, "markdown_source")
    lines, start, paragraph = [], 0, 0
    for number, line in enumerate(text.splitlines(keepends=True), 1):
        if line.strip():
            if not lines:
                start = number
            lines.append(line)
        elif lines:
            paragraph += 1
            output.add("".join(lines), {"kind": "paragraph", "paragraph": paragraph, "startLine": start, "endLine": number - 1})
            lines = []
    if lines:
        paragraph += 1
        output.add("".join(lines), {"kind": "paragraph", "paragraph": paragraph, "startLine": start, "endLine": number})
    output.result["metrics"]["paragraphs"] = paragraph
    return output.finish()


class PDFWarnings(logging.Handler):
    def emit(self, record):
        if record.levelno >= logging.WARNING:
            raise ParseFailure("PDF_PARSE_WARNING")


def inspect_pdf(reader, limits):
    from pypdf.generic import ArrayObject, DictionaryObject, IndirectObject, StreamObject
    stack, seen, count, images = [reader.trailer], set(), 0, False
    while stack:
        value = stack.pop()
        if isinstance(value, IndirectObject):
            identity = ("ref", value.idnum, value.generation)
            if identity in seen:
                continue
            seen.add(identity)
            value = value.get_object()
        if not isinstance(value, (DictionaryObject, ArrayObject)):
            continue
        identity = ("object", id(value))
        if identity in seen:
            continue
        seen.add(identity)
        count += 1
        if count > limits.max_pdf_objects:
            raise ParseFailure("LIMIT_EXCEEDED")
        if isinstance(value, DictionaryObject):
            if any(key in value for key in ("/JS", "/JavaScript", "/EmbeddedFiles", "/RichMedia", "/AA")):
                raise ParseFailure("UNSAFE_DOCUMENT")
            if value.get("/S") in ("/JavaScript", "/Launch", "/GoToR", "/SubmitForm", "/ImportData", "/Rendition"):
                raise ParseFailure("UNSAFE_DOCUMENT")
            if value.get("/Type") == "/Filespec" or (isinstance(value, StreamObject) and "/F" in value):
                raise ParseFailure("UNSAFE_DOCUMENT")
            images |= value.get("/Subtype") == "/Image"
            stack.extend(value.values())
        else:
            stack.extend(value)
        if len(stack) > limits.max_pdf_objects:
            raise ParseFailure("LIMIT_EXCEEDED")
    return images


def pdf_text(data, limits):
    if not data.startswith(b"%PDF-"):
        raise ParseFailure("CORRUPT_DOCUMENT")
    from pypdf import PdfReader, apply_configuration
    from pypdf.errors import DependencyError, FileNotDecryptedError, LimitReachedError, WrongPasswordError
    output = Collector("pdf", limits, "embedded_pdf_text")
    logger = logging.getLogger("pypdf")
    warnings = PDFWarnings()
    logger.addHandler(warnings)
    try:
        # Public, context-local controls in the exact pinned release. These cap
        # decompression even for font/CMap streams used indirectly by extraction.
        with apply_configuration(
            maximum_declared_stream_length=limits.max_pdf_stream_bytes,
            array_based_stream_maximum_output_length=limits.max_pdf_stream_bytes,
            zlib_maximum_output_length=limits.max_pdf_stream_bytes,
            lzw_maximum_output_length=limits.max_pdf_stream_bytes,
            run_length_maximum_output_length=limits.max_pdf_stream_bytes,
            jbig2_maximum_output_length=limits.max_pdf_stream_bytes,
            image_maximum_buffer_size=limits.max_pdf_stream_bytes,
            xform_maximum_invocations_per_extraction=limits.max_pdf_objects,
            page_tree_maximum_entries=limits.max_pdf_objects,
            jbig2dec_binary=None,
        ):
            reader = PdfReader(io.BytesIO(data), strict=True)
            if reader.is_encrypted:
                raise ParseFailure("ENCRYPTED_DOCUMENT")
            images = inspect_pdf(reader, limits)
            if len(reader.pages) > limits.max_pages:
                raise ParseFailure("LIMIT_EXCEEDED")
            total_stream_bytes = 0
            for number, page in enumerate(reader.pages, 1):
                contents = page.get_contents()
                if contents is not None:
                    size = len(contents.get_data())
                    total_stream_bytes += size
                    if size > limits.max_pdf_stream_bytes or total_stream_bytes > limits.max_zip_bytes:
                        raise ParseFailure("LIMIT_EXCEEDED")
                text = page.extract_text() or ""
                if not output.add(text, {"kind": "page", "page": number}):
                    output.warn("NO_TEXT_ON_PAGE", {"page": number, "mayRequireOCR": True})
            output.result["metrics"]["pages"] = len(reader.pages)
            reader.close()
            return output.finish("OCR_REQUIRED" if images else "NO_EXTRACTABLE_TEXT")
    except (FileNotDecryptedError, WrongPasswordError, DependencyError):
        raise ParseFailure("ENCRYPTED_DOCUMENT") from None
    except LimitReachedError:
        raise ParseFailure("LIMIT_EXCEEDED") from None
    finally:
        logger.removeHandler(warnings)
