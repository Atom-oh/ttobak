import unittest
import io
import struct
import zipfile
from dataclasses import replace

from fixtures import A, P, R, REL, W, archive, docx_parts, pptx_parts, pdf
from contract import Limits
from parsers import extract_bytes


class FormatTests(unittest.TestCase):
    def test_markdown_strict_utf8_and_original_lines(self):
        result = extract_bytes("# 제목😀\n첫 문단\n\n다음 한글".encode(), "md")
        self.assertEqual(result["status"], "succeeded")
        self.assertEqual([u["text"] for u in result["units"]], ["# 제목😀\n첫 문단", "다음 한글"])
        self.assertEqual(result["units"][1]["location"], {"kind": "paragraph", "paragraph": 2, "startLine": 4, "endLine": 4})
        self.assertEqual(extract_bytes(b"\xff", "md")["error"]["code"], "INVALID_ENCODING")

    def test_pdf_native_korean_page_provenance(self):
        result = extract_bytes(pdf(("한글 메모😀", "다음 페이지")), "pdf")
        self.assertEqual(result["status"], "succeeded", result)
        self.assertEqual([u["text"] for u in result["units"]], ["한글 메모😀", "다음 페이지"])
        self.assertEqual([u["location"]["page"] for u in result["units"]], [1, 2])

    def test_docx_body_and_table_provenance(self):
        result = extract_bytes(archive(docx_parts()), "docx")
        self.assertEqual(result["status"], "succeeded", result)
        self.assertEqual([u["text"] for u in result["units"]], ["한글 메모😀", "셀 내용", "두 번째"])
        self.assertEqual(result["units"][1]["location"]["table"], 1)
        self.assertEqual(result["units"][1]["location"]["row"], 1)
        self.assertEqual(result["units"][2]["location"]["cell"], 2)
        self.assertTrue(all("page" not in u["location"] for u in result["units"]))

    def test_presentation_relationship_order_not_filename_or_zip_order(self):
        result = extract_bytes(archive(pptx_parts()), "pptx")
        self.assertEqual(result["status"], "succeeded", result)
        self.assertEqual([u["text"] for u in result["units"]], ["먼저 슬라이드😀", "나중 슬라이드"])
        self.assertEqual([u["location"]["slide"] for u in result["units"]], [1, 2])

    def test_empty_scanned_encrypted_and_corrupt_are_not_success(self):
        for data, fmt in [(b"", "md"), (b" \n", "md"), (b"broken", "pdf"), (b"broken", "docx"), (b"broken", "pptx")]:
            result = extract_bytes(data, fmt)
            self.assertEqual(result["status"], "failed", result)
            self.assertFalse(result["complete"])
            self.assertEqual(result["units"], [])
        self.assertEqual(extract_bytes(pdf(encrypted=True), "pdf")["error"]["code"], "ENCRYPTED_DOCUMENT")
        self.assertEqual(extract_bytes(pdf((None,)), "pdf")["error"]["code"], "OCR_REQUIRED")

    def test_limits_fail_without_silent_truncation(self):
        for data, fmt, limits in [
            (b"abcdef", "md", replace(Limits(), max_input_bytes=5)),
            ("한글".encode(), "md", replace(Limits(), max_text_bytes=3)),
            (pdf(("a", "b")), "pdf", replace(Limits(), max_pages=1)),
            (archive(pptx_parts()), "pptx", replace(Limits(), max_slides=1)),
        ]:
            result = extract_bytes(data, fmt, limits)
            self.assertEqual(result["status"], "failed", result)
            self.assertEqual(result["error"]["code"], "LIMIT_EXCEEDED")
            self.assertEqual(result["units"], [])

    def test_empty_office_content_is_not_metadata_or_filename_success(self):
        parts = docx_parts()
        parts["word/document.xml"] = f'<w:document xmlns:w="{W}"><w:body><w:p/></w:body></w:document>'
        self.assertEqual(extract_bytes(archive(parts), "docx")["error"]["code"], "NO_EXTRACTABLE_TEXT")
        parts = pptx_parts()
        parts["ppt/presentation.xml"] = f'<p:presentation xmlns:p="{P}"/>'
        self.assertEqual(extract_bytes(archive(parts), "pptx")["error"]["code"], "NO_EXTRACTABLE_TEXT")


class SafetyTests(unittest.TestCase):
    def failed(self, data, fmt="docx", code="UNSAFE_DOCUMENT", limits=None):
        result = extract_bytes(data, fmt, limits)
        self.assertEqual(result["status"], "failed", result)
        self.assertFalse(result["complete"])
        self.assertEqual(result["units"], [])
        self.assertEqual(result["error"]["code"], code, result)

    def test_archive_paths_symlinks_and_case_collisions(self):
        for name in ["../evil.xml", "/tmp/evil.xml", "word/../evil.xml", "word\\evil.xml", "word/%2e%2e/evil.xml", "C:/evil"]:
            with self.subTest(name=name):
                self.failed(archive({**docx_parts(), name: "x"}))
        self.failed(archive({**docx_parts(), "WORD/DOCUMENT.XML": "<x/>"}))
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w") as z:
            for name, value in docx_parts().items():
                z.writestr(name, value)
            link = zipfile.ZipInfo("word/link")
            link.external_attr = (0o120777 << 16)
            z.writestr(link, "/etc/passwd")
        self.failed(buffer.getvalue())

    def test_external_relationships_and_escape_targets(self):
        for target, mode in [("https://example.invalid/doc", "External"), ("file:///etc/passwd", "Internal"), ("../../outside", "Internal")]:
            parts = docx_parts()
            parts["word/_rels/document.xml.rels"] = f'<Relationships xmlns="{REL}"><Relationship Id="bad" Type="{R}/attachedTemplate" Target="{target}" TargetMode="{mode}"/></Relationships>'
            self.failed(archive(parts))

    def test_ordinary_external_hyperlinks_preserve_visible_text_without_fetching_targets(self):
        parts = docx_parts()
        parts["word/document.xml"] = f'<w:document xmlns:w="{W}" xmlns:r="{R}"><w:body><w:p><w:hyperlink r:id="link"><w:r><w:t>외부 문서 설명</w:t></w:r></w:hyperlink></w:p></w:body></w:document>'
        parts["word/_rels/document.xml.rels"] = f'<Relationships xmlns="{REL}"><Relationship Id="link" Type="{R}/hyperlink" Target="https://example.invalid/not-fetched?q=%20#section" TargetMode="External"/></Relationships>'
        result = extract_bytes(archive(parts), "docx")
        self.assertEqual(result["status"], "succeeded", result)
        self.assertEqual([u["text"] for u in result["units"]], ["외부 문서 설명"])
        self.assertNotIn("not-fetched", str(result))
        parts = pptx_parts()
        parts["ppt/slides/slide10.xml"] = parts["ppt/slides/slide10.xml"].replace(
            "<p:sld ", f'<p:sld xmlns:r="{R}" ').replace("<a:r>", '<a:r><a:rPr><a:hlinkClick r:id="link"/></a:rPr>')
        parts["ppt/slides/_rels/slide10.xml.rels"] = f'<Relationships xmlns="{REL}"><Relationship Id="link" Type="{R}/hyperlink" Target="mailto:contact@example.invalid" TargetMode="External"/></Relationships>'
        result = extract_bytes(archive(parts), "pptx")
        self.assertEqual(result["status"], "succeeded", result)
        self.assertEqual(result["units"][0]["location"]["slide"], 1)
        self.assertNotIn("contact@example", str(result))

    def test_entities_even_in_unreferenced_uppercase_xml(self):
        for declaration in ['<!ENTITY x "boom">', '<!ENTITY x SYSTEM "file:///etc/passwd">']:
            parts = docx_parts()
            parts["custom/Evil.XML"] = f'<!DOCTYPE x [{declaration}]><x>&x;</x>'
            self.failed(archive(parts))

    def test_macro_parts_types_and_pdf_javascript(self):
        self.failed(archive({**docx_parts(), "word/vbaProject.bin": b"macro"}))
        parts = docx_parts()
        parts["[Content_Types].xml"] = parts["[Content_Types].xml"].replace(
            "wordprocessingml.document.main+xml", "word.document.macroEnabled.main+xml")
        self.failed(archive(parts))
        self.failed(pdf(javascript=True), "pdf")
        self.failed(bytes.fromhex("d0cf11e0a1b11ae1") + b"fake", code="ENCRYPTED_OR_LEGACY_OFFICE")

    def test_encrypted_zip_and_crc_corruption(self):
        data = bytearray(archive(docx_parts()))
        for signature, flag_offset in [(b"PK\x03\x04", 6), (b"PK\x01\x02", 8)]:
            position = data.index(signature)
            flags = struct.unpack_from("<H", data, position + flag_offset)[0]
            struct.pack_into("<H", data, position + flag_offset, flags | 1)
        self.failed(bytes(data), code="ENCRYPTED_DOCUMENT")
        out = io.BytesIO()
        with zipfile.ZipFile(out, "w", zipfile.ZIP_STORED) as z:
            for name, body in docx_parts().items():
                z.writestr(name, body)
        data = bytearray(out.getvalue())
        data[data.index("한글".encode())] ^= 1
        self.failed(bytes(data), code="CORRUPT_DOCUMENT")

    def test_zip_xml_pdf_and_output_limits(self):
        cases = [
            (archive({**docx_parts(), "word/padding.bin": b"x" * 100000}), "docx", Limits()),
            (archive(docx_parts()), "docx", replace(Limits(), max_entries=2)),
            (archive(docx_parts()), "docx", replace(Limits(), max_zip_bytes=100)),
            (archive(docx_parts()), "docx", replace(Limits(), max_xml_bytes=50)),
            (archive(docx_parts()), "docx", replace(Limits(), max_xml_nodes=2)),
            (archive(docx_parts()), "docx", replace(Limits(), max_xml_depth=2)),
            (b"a\n\nb", "md", replace(Limits(), max_units=1)),
            (b"\x00" * 300, "md", replace(Limits(), max_result_bytes=1024)),
            (pdf(content_bytes=b"x" * 10000, compress=True), "pdf", replace(Limits(), max_pdf_stream_bytes=1000)),
        ]
        for data, fmt, limits in cases:
            with self.subTest(fmt=fmt, limits=limits):
                self.failed(data, fmt, "LIMIT_EXCEEDED", limits)

    def test_partial_pdf_and_missing_slide_relationship(self):
        result = extract_bytes(pdf(("본문", None)), "pdf")
        self.assertEqual(result["status"], "partial", result)
        self.assertFalse(result["complete"])
        self.assertEqual(result["warnings"][0]["location"]["page"], 2)
        parts = pptx_parts()
        del parts["ppt/slides/slide10.xml"]
        self.failed(archive(parts), "pptx", "CORRUPT_DOCUMENT")

    def test_docx_wrapped_rows_nested_tables_and_pptx_table_text(self):
        parts = docx_parts()
        parts["word/document.xml"] = f'''<w:document xmlns:w="{W}"><w:body><w:tbl>
        <w:sdt><w:sdtContent><w:tr><w:tc><w:p><w:r><w:t>바깥</w:t></w:r></w:p>
        <w:tbl><w:tr><w:tc><w:p><w:r><w:t>안쪽</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
        </w:tc></w:tr></w:sdtContent></w:sdt></w:tbl></w:body></w:document>'''
        result = extract_bytes(archive(parts), "docx")
        self.assertEqual([u["text"] for u in result["units"]], ["바깥", "안쪽"], result)
        self.assertEqual([u["location"]["table"] for u in result["units"]], [1, 2])
        parts = pptx_parts()
        parts["ppt/slides/slide10.xml"] = f'''<p:sld xmlns:p="{P}" xmlns:a="{A}"><p:cSld><p:spTree>
        <p:graphicFrame><a:graphic><a:graphicData><a:tbl><a:tr><a:tc><a:txBody><a:p><a:r>
        <a:t>슬라이드 표😀</a:t></a:r></a:p></a:txBody></a:tc></a:tr></a:tbl></a:graphicData></a:graphic>
        </p:graphicFrame></p:spTree></p:cSld></p:sld>'''
        result = extract_bytes(archive(parts), "pptx")
        self.assertEqual(result["units"][0]["text"], "슬라이드 표😀", result)
        self.assertEqual(result["units"][0]["location"]["slide"], 1)
        self.assertEqual(result["units"][0]["location"]["cell"], 1)


if __name__ == "__main__":
    unittest.main()
