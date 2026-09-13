# Attachment text parser

Extract native text from PDF, PPTX, DOCX and UTF-8 Markdown. The parser has no
AWS client, OCR, LLM or network retrieval, and cannot succeed from filenames
or metadata alone. The [Lambda parent](LAMBDA.md) and infrastructure exist;
upload, summary, active QA and UI integration remain separate rollout work.

## Entry points and output

- `parsers.py`: format dispatch, Markdown paragraphs and PDF page text.
- `ooxml.py`: bounded ZIP/XML validation, relationships, DOCX body/table text
  and PPTX slide/table text.
- `contract.py`: immutable limits, text units and explicit result states.
- `worker.py`: bounded parent protocol and resource-limited child. Untrusted
  files must use this runner; direct `extract_bytes` calls impose no OS limits.

Results contain `schemaVersion`, `format`, `status` (`succeeded`, `partial`,
`failed`), `complete`, `scope`, `units`, `warnings`, `error` and `metrics`.
Each nonempty unit has text and one-based source locations: PDF page, PPTX
slide/paragraph, DOCX body paragraph with table/row/cell where applicable, or
Markdown paragraph/source lines. No DOCX page numbers or PDF table structure
are inferred. PPTX order follows presentation relationships.

Completeness covers declared native text, not rendered appearance, OCR,
images, slide masters/notes or DOCX headers/footnotes/comments. Missing text
on PDF pages/PPTX slides is explicit; mixed documents may be partial.
Empty/image-only input never succeeds with zero units. Corruption, unsafe
content or exceeded limits discards partial output and fails.

## Usage and limits

Use Python 3.12 and the exact pins in `requirements.txt`, from this directory:

```bash
python3 -m pip install -r requirements.txt
python3 worker.py /trusted/staging/attachment.pdf
python3 worker.py /trusted/staging/file --format docx
python3 -m unittest test_extract test_worker -v
```

Exit 0 means complete extraction; 2 means partial/failed extraction. Stdout
is one bounded UTF-8 JSON object. The staging path is trusted parent input;
symlinks/non-regular files are rejected and archive members never reach disk.
A storage parent can call `run_parser(data: bytes, fmt, limits)`.

| Limit | Default ceiling |
|---|---:|
| Input/compressed bytes | 20 MiB |
| ZIP total uncompressed / one member | 64 MiB / 16 MiB |
| ZIP members / expansion ratio | 2048 / 200:1 |
| One XML part / total nodes / depth | 8 MiB / 200,000 / 64 |
| PDF pages / PPTX slides | 200 / 200 |
| PDF decoded stream / aggregate page content | 8 MiB / 64 MiB |
| PDF reachable containers / pending graph entries | 50,000 |
| Text units / extracted UTF-8 bytes | 4000 / 256 KiB |
| Serialized JSON / stderr | 1 MiB / 16 KiB |
| Child virtual address space | 512 MiB |
| Child CPU soft/hard / parent wall deadline | 8 s / 9 s / 12 s |

`Limits` may lower ceilings, never raise or disable them. Output accounting
includes actual JSON escaping; final serialization checks the full envelope.
ZIP metadata is checked before bounded reads validate every member's length
and CRC. The pinned pypdf configuration bounds decompression, including fonts
and CMaps, and disables external JBIG2 execution. OS limits remain the backstop.
Budget parent and child memory separately; address space is not resident RAM.

## Supported scope and exclusions

- PDF uses native text/page order, without OCR or visual reading-order/table
  reconstruction. Image-only files fail with `OCR_REQUIRED`, truly empty
  files with `NO_EXTRACTABLE_TEXT`; mixed missing-text pages remain partial.
- OOXML supports transitional namespaces. Strict/unknown namespaces,
  encrypted/legacy OLE containers, encrypted ZIP entries, macros, ActiveX,
  OLE and embedded packages are rejected. The sole embedded-package exception
  is an internal XLSX workbook under `ppt/embeddings/` referenced by a PPTX
  chart: it stays opaque and omitted, making extraction partial. This is not
  an antivirus scan of hidden bytes.
- Ordinary DOCX/PPTX hyperlink relationships are inert. Preserve visible run
  text but never fetch, execute, emit or resolve targets as package parts.
  Remote templates/images and other external relationships remain rejected.
  Ambiguous/encoded package paths fail; internal `../` resolves only within
  the archive, never the filesystem. Extraction is not renderer sanitization.
- DOCX traverses body paragraphs/table cells, including empty paragraphs in
  numbering; deleted/moved-from runs are excluded and nonbreaking/soft
  hyphens preserved. Table indices are XML ordinals, not visual coordinates.
- PPTX follows slide relationship order and XML shape order. Masters,
  layouts, speaker notes and separate chart/diagram data are excluded;
  encountered unsupported text structures produce warnings.
- Markdown preserves raw paragraph markup/source lines without rendering or
  fetching URLs, images, HTML or code. Optional UTF-8 BOM is accepted; invalid
  UTF-8 fails. Outer whitespace is trimmed, so this is not a byte round trip.

The child strips inherited environment/credentials, sets address-space,
CPU and core-file limits before dependency import/input reads, and uses an
OS-supported process group plus Python network/subprocess audit hooks.
The parent bounds wall time, stdout and stderr. Fixed error strings exclude
tracebacks/source text. These controls are not an RCE-proof filesystem or
container sandbox; deployment owns IAM, storage and network isolation.

## Verification

`test_extract.py` and `test_worker.py` use generated multilingual documents
and temporary files. Coverage includes text locations/order, inert hyperlinks,
opaque chart workbooks, encryption/corruption, ZIP/XML/macro attacks, real child
resource limits, sanitized environment, bounded protocol and CLI output.
These local tests do not establish deployed isolation or end-to-end extraction.
