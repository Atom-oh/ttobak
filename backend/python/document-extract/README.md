# Attachment text parser foundation

This module extracts existing text from PDF, PPTX, DOCX, and UTF-8 Markdown.
The parser has no AWS client, OCR, LLM, or network retrieval.
Filename/metadata alone can never produce successful extraction.

## Design

- `parsers.py`: format dispatch, Markdown source paragraphs, PDF page text.
- `ooxml.py`: bounded ZIP/XML validation, package relationships, DOCX body
  paragraphs/table cells and PPTX slide paragraphs/table cells.
- `contract.py`: immutable limits, bounded text units, explicit result states.
- `worker.py`: resource-limited child and bounded parent protocol. Use this entry
  point for untrusted files; direct parsing is for tests/trusted callers.

Results carry `schemaVersion`, `format`, `status` (`succeeded`, `partial`,
`failed`), `complete`, `scope`, `units`, `warnings`, `error`, and `metrics`.
Each nonempty unit contains text and a one-based `location`: PDF page, PPTX slide
and paragraph, DOCX body paragraph (plus table/row/cell where applicable), or
Markdown paragraph and source line range. DOCX page numbers and PDF paragraph/
table structure are not inferred. PPTX order follows presentation relationships.

Completeness refers only to the declared text scope, not rendered appearance,
images, OCR correctness, slide masters/notes, or DOCX headers/footnotes/comments.
Missing native text on a PDF page or PPTX slide is explicit; mixed documents can
return `partial`. Empty/image-only documents fail, never succeed with no units.
Corruption, unsafe content, or exceeded limits discard partial output and fail.

The parent runner enforces a wall deadline and bounded stdout/stderr. The child
sets OS address-space/CPU/core-file limits before importing parser dependencies
and reading input. It strips inherited environment/credentials and blocks normal
Python network/subprocess operations through an audit hook. These are process
resource controls, not an RCE-proof filesystem/container sandbox; a later
deployment still owns OS isolation, no-egress policy, IAM, and storage access.

## Implementation plan

1. Add generated fixtures and observe failures for all four format contracts.
2. Implement strict package/text parsing, truthful provenance and result limits.
3. Test reordered slide relationships, Korean text, encrypted/corrupt inputs,
   ZIP/XML attacks, resource limits and child-protocol limits.
4. Run stdlib unittest under Python 3.12 and document measured checks.

Queue producers, AWS integration, infrastructure, summary/QA/UI integration,
and production deployment remain separate from the pure parser.

## Usage and limits

Use Python 3.12, install the exact pins, then invoke the parent runner:

```bash
python -m pip install -r requirements.txt
python worker.py /trusted/staging/attachment.pdf
python worker.py /trusted/staging/file --format docx
python -m unittest test_extract test_worker -v
```

CLI exit code is 0 for complete extraction, 2 for partial/failed extraction.
Stdout is one bounded UTF-8 JSON object. The CLI's staging path is trusted parent
input, not a path extracted from a ZIP. Symlinks and non-regular files are rejected.
No package member is written to disk. A storage parent can import
`run_parser(data: bytes, fmt, limits)` and pass already-downloaded bytes.

| Limit | Default |
|---|---:|
| Input/compressed file bytes | 20 MiB |
| ZIP total declared uncompressed bytes | 64 MiB |
| One ZIP member | 16 MiB |
| ZIP members / expansion ratio | 2048 / 200:1 |
| One XML part | 8 MiB |
| XML nodes (whole package) / depth | 200,000 / 64 |
| PDF pages / presentation slides | 200 / 200 |
| PDF decoded-stream / aggregate page-content bytes | 8 MiB / 64 MiB |
| PDF reachable containers / pending graph entries | 50,000 |
| Text units / extracted UTF-8 bytes | 4000 / 256 KiB |
| Serialized JSON / stderr | 1 MiB / 16 KiB |
| Child virtual address space | 512 MiB |
| Child CPU soft/hard / parent wall time | 8 s / 9 s / 12 s |

`Limits` can lower these ceilings, not raise/disable them. Limits count actual
encoded output (including JSON escaping), not just character counts. ZIP metadata
is checked before decompression; every member is read under bounds to validate its
actual length and CRC. PDF decompression uses the pinned release's public
`apply_configuration` controls, including font/CMap streams and no external
JBIG2 binary. Resource controls remain the backstop during library parsing.
Virtual address space is not resident memory: the tested CPython build already
reserves roughly 245 MiB before parser work. A deployment must budget both parent
and child memory rather than equating this ceiling with Lambda memory size.

## Supported scope and explicit exclusions

- PDF extraction uses native text and page order. It does not OCR, reconstruct
  tables/reading order, or interpret pictures. A page without text is flagged;
  an image-only PDF fails with `OCR_REQUIRED`. A truly empty PDF fails with
  `NO_EXTRACTABLE_TEXT`. Mixed text/image-only pages return `partial`.
- OOXML supports the usual transitional namespaces. Strict/other namespaces,
  encrypted/legacy OLE containers, encrypted ZIP members, macro-enabled content
  types/parts/relationships, ActiveX, OLE objects, and embedded packages are rejected.
  This is not an antivirus scan of arbitrary hidden bytes.
- Ordinary external `.../relationships/hyperlink` relationships are accepted as
  inert data in DOCX/PPTX. Visible run text is preserved. Targets (including
  HTTPS, mailto, file or other schemes) are never fetched, executed, emitted in
  the result, or resolved as package parts. This is text extraction, not link
  sanitization for a renderer. Other external relationships, including remote
  templates, linked OLE/ActiveX and remote images, remain rejected. Encoded or
  ambiguous package paths are rejected; internal `../` relationships resolve
  only inside the archive, never to the filesystem.
- DOCX includes current body paragraphs and table cells; paragraph numbering
  follows body traversal and includes empty paragraphs. Deleted runs are excluded.
  Table/cell indices are XML ordinals, not inferred visual columns or page numbers.
- PPTX includes slide body paragraphs/tables in presentation relationship order.
  Paragraph order is XML shape order, not a guess at visual reading order.
  Slide masters, layouts, speaker notes, and separate chart/diagram data are not
  imported. Encountered unsupported alternate/embedded text structures are flagged.
- Markdown retains raw markup as paragraph text, with source line ranges. It is
  not rendered; URLs, images, HTML and code are never fetched or executed. Optional
  UTF-8 BOM is accepted; invalid UTF-8 is rejected. Outer paragraph whitespace is
  trimmed, so text extraction is not a byte-for-byte source-file round trip.
- The parser has no filename-derived text fallback, decryption/password interface,
  network client, or dynamic shell command. Error messages are fixed strings,
  never library traceback/source-content dumps.

Direct `extract_bytes` calls do not establish OS resource limits. Production
integration must use the child runner on a platform supporting `resource` and
POSIX process groups. It must also provide a suitably isolated filesystem and
network boundary if protection against native-code/RCE exploits is required.

## Primary references

- pypdf text extraction:
  https://pypdf.readthedocs.io/en/stable/user/extract-text.html
- pypdf strict parsing:
  https://pypdf.readthedocs.io/en/stable/user/robustness.html
- Primary documentation/source verified directly at the publisher:
  https://raw.githubusercontent.com/py-pdf/pypdf/main/docs/user/extract-text.md
  and https://raw.githubusercontent.com/py-pdf/pypdf/main/docs/user/robustness.md
- Exact pinned public decompression-configuration implementation:
  https://raw.githubusercontent.com/py-pdf/pypdf/6.18.1/pypdf/_configuration.py
- Dependency versions resolved from the publishers' PyPI JSON metadata:
  https://pypi.org/pypi/pypdf/json and https://pypi.org/pypi/defusedxml/json
- defusedxml flags and XML threat model:
  https://github.com/tiran/defusedxml
- Open XML slide relationships:
  https://learn.microsoft.com/en-us/office/open-xml/presentation/working-with-presentation-slides
- Python resource limits:
  https://docs.python.org/3.12/library/resource.html

The public documentation cache can lag the package version; the exact installed
pin is exercised by the generated PDF fixtures. Encryption is rejected rather
than decrypted; no optional crypto/image/font/OCR dependency is installed.

## Verification

The 23 parser/child tests pass under CPython 3.12.13 on Linux/AArch64.
Coverage includes generated Korean PDF text,
presentation relationship ordering, DOCX nested/wrapped tables, Markdown UTF-8,
encrypted/corrupt/empty/scanned files, unsafe ZIP/XML/macro content, configured
limits, real child CPU/address-space/wall limits, bounded stdout/stderr, sanitized
environment, ordinary DOCX/PPTX hyperlinks, and CLI JSON. Tests use generated
data and home-cache temporary directories, not customer files or live AWS.
