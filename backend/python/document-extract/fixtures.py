"""Small generated test documents; no customer files or network access."""
import io
import zipfile
from xml.sax.saxutils import escape

from pypdf import PdfWriter
from pypdf.generic import (
    ArrayObject, DecodedStreamObject, DictionaryObject, NameObject,
    NumberObject, TextStringObject,
)

REL = "http://schemas.openxmlformats.org/package/2006/relationships"
R = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
P = "http://schemas.openxmlformats.org/presentationml/2006/main"
A = "http://schemas.openxmlformats.org/drawingml/2006/main"
CT = "http://schemas.openxmlformats.org/package/2006/content-types"


def archive(parts):
    out = io.BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
        for name, content in parts.items():
            z.writestr(name, content)
    return out.getvalue()


def docx_parts():
    return {
        "[Content_Types].xml": f'<Types xmlns="{CT}"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>',
        "_rels/.rels": f'<Relationships xmlns="{REL}"><Relationship Id="root" Type="{R}/officeDocument" Target="word/document.xml"/></Relationships>',
        "word/document.xml": f'''<w:document xmlns:w="{W}"><w:body>
          <w:p><w:r><w:t>한글 메모😀</w:t></w:r></w:p>
          <w:tbl><w:tr><w:tc><w:p><w:r><w:t>셀 내용</w:t></w:r></w:p></w:tc>
          <w:tc><w:p><w:r><w:t>두 번째</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
        </w:body></w:document>''',
    }


def pptx_parts():
    def slide(text):
        return f'<p:sld xmlns:p="{P}" xmlns:a="{A}"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>{escape(text)}</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>'
    return {
        "[Content_Types].xml": f'<Types xmlns="{CT}"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/><Override PartName="/ppt/slides/slide2.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/><Override PartName="/ppt/slides/slide10.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/></Types>',
        "_rels/.rels": f'<Relationships xmlns="{REL}"><Relationship Id="root" Type="{R}/officeDocument" Target="ppt/presentation.xml"/></Relationships>',
        "ppt/presentation.xml": f'<p:presentation xmlns:p="{P}" xmlns:r="{R}"><p:sldIdLst><p:sldId id="256" r:id="r10"/><p:sldId id="257" r:id="r2"/></p:sldIdLst></p:presentation>',
        "ppt/_rels/presentation.xml.rels": f'<Relationships xmlns="{REL}"><Relationship Id="r2" Type="{R}/slide" Target="slides/slide2.xml"/><Relationship Id="r10" Type="{R}/slide" Target="slides/slide10.xml"/></Relationships>',
        "ppt/slides/slide2.xml": slide("나중 슬라이드"),
        "ppt/slides/slide10.xml": slide("먼저 슬라이드😀"),
    }


def pdf(pages=("한글 메모😀",), encrypted=False, javascript=False, content_bytes=None, compress=False):
    writer = PdfWriter()
    for text in pages:
        page = writer.add_blank_page(300, 300)
        if text is None:
            image = DecodedStreamObject()
            image.set_data(bytes([255, 255, 255]))
            image.update({
                NameObject("/Type"): NameObject("/XObject"),
                NameObject("/Subtype"): NameObject("/Image"),
                NameObject("/Width"): NumberObject(1), NameObject("/Height"): NumberObject(1),
                NameObject("/ColorSpace"): NameObject("/DeviceRGB"),
                NameObject("/BitsPerComponent"): NumberObject(8),
            })
            page[NameObject("/Resources")] = DictionaryObject({
                NameObject("/XObject"): DictionaryObject({NameObject("/Im"): writer._add_object(image)}),
            })
            content = b"q 100 0 0 100 0 0 cm /Im Do Q"
        else:
            # Explicit ToUnicode glyph mapping exercises real Korean extraction
            # without depending on local fonts, OCR, or report-generation packages.
            unique = list(dict.fromkeys(text))
            codes = {char: i + 1 for i, char in enumerate(unique)}
            mapping = "\n".join(f"<{codes[c]:04x}> <{c.encode('utf-16-be').hex()}>" for c in unique)
            cmap = DecodedStreamObject()
            cmap.set_data((
                "/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n"
                "/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n"
                "/CMapName /Fixture def /CMapType 2 def\n"
                "1 begincodespacerange <0000> <ffff> endcodespacerange\n"
                f"{len(unique)} beginbfchar\n{mapping}\nendbfchar\n"
                "endcmap CMapName currentdict /CMap defineresource pop end end"
            ).encode("ascii"))
            descendant = DictionaryObject({
                NameObject("/Type"): NameObject("/Font"),
                NameObject("/Subtype"): NameObject("/CIDFontType2"),
                NameObject("/BaseFont"): NameObject("/Fixture"),
                NameObject("/CIDSystemInfo"): DictionaryObject({
                    NameObject("/Registry"): TextStringObject("Adobe"),
                    NameObject("/Ordering"): TextStringObject("Identity"),
                    NameObject("/Supplement"): NumberObject(0),
                }),
                NameObject("/DW"): NumberObject(1000),
            })
            font = DictionaryObject({
                NameObject("/Type"): NameObject("/Font"), NameObject("/Subtype"): NameObject("/Type0"),
                NameObject("/BaseFont"): NameObject("/Fixture"),
                NameObject("/Encoding"): NameObject("/Identity-H"),
                NameObject("/DescendantFonts"): ArrayObject([writer._add_object(descendant)]),
                NameObject("/ToUnicode"): writer._add_object(cmap),
            })
            page[NameObject("/Resources")] = DictionaryObject({
                NameObject("/Font"): DictionaryObject({NameObject("/F1"): writer._add_object(font)}),
            })
            glyphs = "".join(f"{codes[c]:04x}" for c in text)
            content = f"BT /F1 12 Tf 10 250 Td <{glyphs}> Tj ET".encode("ascii")
        stream = DecodedStreamObject()
        stream.set_data(content_bytes if content_bytes is not None else content)
        if compress:
            stream = stream.flate_encode()
        page[NameObject("/Contents")] = writer._add_object(stream)
    if encrypted:
        writer.encrypt("synthetic-password")
    if javascript:
        writer.add_js("app.alert('fixture')")
    output = io.BytesIO()
    writer.write(output)
    return output.getvalue()
