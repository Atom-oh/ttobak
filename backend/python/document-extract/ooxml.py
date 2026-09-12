"""OOXML text without filesystem extraction, relationship fetching, or XML entities."""
import io
import posixpath
import stat
import zipfile
from urllib.parse import urlsplit

from defusedxml import ElementTree
from defusedxml.common import DefusedXmlException

from contract import Collector, ParseFailure

REL = "http://schemas.openxmlformats.org/package/2006/relationships"
R = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
P = "http://schemas.openxmlformats.org/presentationml/2006/main"
A = "http://schemas.openxmlformats.org/drawingml/2006/main"
CT = "http://schemas.openxmlformats.org/package/2006/content-types"
MC = "http://schemas.openxmlformats.org/markup-compatibility/2006"


def q(namespace, name):
    return "{" + namespace + "}" + name


class Package:
    def __init__(self, data, limits):
        if data.startswith(bytes.fromhex("d0cf11e0a1b11ae1")):
            raise ParseFailure("ENCRYPTED_OR_LEGACY_OFFICE")
        self.limits, self.xml, self.rels = limits, {}, {}
        self.zip = zipfile.ZipFile(io.BytesIO(data))
        infos = self.zip.infolist()
        if len(infos) > limits.max_entries:
            raise ParseFailure("LIMIT_EXCEEDED")
        self.names = {}
        total, folded, nodes = 0, set(), 0
        for info in infos:
            name = info.filename
            plain = name.rstrip("/")
            if (info.orig_filename != name or not plain or len(name.encode()) > 512 or
                    name.startswith("/") or any(c in name for c in ("\\", ":", "%", "\x00", "?", "#")) or
                    any(part in ("", ".", "..") for part in plain.split("/")) or
                    stat.S_ISLNK(info.external_attr >> 16) or plain.casefold() in folded):
                raise ParseFailure("UNSAFE_DOCUMENT")
            folded.add(plain.casefold())
            if info.flag_bits & 1:
                raise ParseFailure("ENCRYPTED_DOCUMENT")
            if info.compress_type not in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED):
                raise ParseFailure("UNSUPPORTED_FORMAT")
            total += info.file_size
            if (total > limits.max_zip_bytes or info.file_size > limits.max_entry_bytes or
                    info.file_size > max(1, info.compress_size) * limits.max_zip_ratio):
                raise ParseFailure("LIMIT_EXCEEDED")
            lower = name.lower()
            if ("vba" in lower or "activex" in lower or
                    lower.endswith((".docm", ".pptm", ".xlsm", ".ppam", ".xlam"))):
                raise ParseFailure("UNSAFE_DOCUMENT")
            if not info.is_dir():
                self.names[name] = info
        for name, info in self.names.items():
            # Reading every member verifies CRC/actual length, even on an
            # unreferenced part. No extract()/extractall() or filesystem paths.
            with self.zip.open(info) as stream:
                body = stream.read(min(info.file_size, limits.max_entry_bytes) + 1)
            if len(body) != info.file_size:
                raise ParseFailure("CORRUPT_DOCUMENT")
            if name.lower().endswith((".xml", ".rels")):
                if len(body) > limits.max_xml_bytes:
                    raise ParseFailure("LIMIT_EXCEEDED")
                try:
                    root = ElementTree.fromstring(body, forbid_dtd=True, forbid_entities=True, forbid_external=True)
                except DefusedXmlException:
                    raise ParseFailure("UNSAFE_DOCUMENT") from None
                stack = [(root, 1)]
                while stack:
                    node, depth = stack.pop()
                    nodes += 1
                    if nodes > limits.max_xml_nodes or depth > limits.max_xml_depth:
                        raise ParseFailure("LIMIT_EXCEEDED")
                    if node.tag.startswith("{http://www.w3.org/2001/XInclude}"):
                        raise ParseFailure("UNSAFE_DOCUMENT")
                    stack.extend((child, depth + 1) for child in node)
                self.xml[name] = root
        for name, root in self.xml.items():
            if name.lower().endswith(".rels"):
                self.rels[self.relationship_source(name)] = self.relationships(root, self.relationship_source(name))
        types = self.xml.get("[Content_Types].xml")
        if types is None or types.tag != q(CT, "Types"):
            raise ParseFailure("CORRUPT_DOCUMENT")
        self.types = {}
        for item in types:
            content_type = item.get("ContentType", "")
            if any(marker in content_type.lower() for marker in ("macroenabled", "vbaproject", "activex", "oleobject")):
                raise ParseFailure("UNSAFE_DOCUMENT")
            if item.tag == q(CT, "Override"):
                part = self.target("", item.get("PartName", ""))
                if part in self.types:
                    raise ParseFailure("CORRUPT_DOCUMENT")
                self.types[part] = content_type

    def relationship_source(self, name):
        if name == "_rels/.rels":
            return ""
        folder, leaf = posixpath.split(name)
        if posixpath.basename(folder) != "_rels" or not leaf.lower().endswith(".rels"):
            raise ParseFailure("CORRUPT_DOCUMENT")
        source = posixpath.join(posixpath.dirname(folder), leaf[:-5])
        if source not in self.names:
            raise ParseFailure("CORRUPT_DOCUMENT")
        return source

    def target(self, source, target):
        if (not target or any(c in target for c in ("\\", "%", "\x00", "?", "#", ":")) or
                target.startswith("//") or urlsplit(target).scheme or urlsplit(target).netloc):
            raise ParseFailure("UNSAFE_DOCUMENT")
        # ../ is normal for internal slide layouts/media; resolve only inside
        # the package. Archive entry names themselves never contain traversal.
        resolved = posixpath.normpath(target.lstrip("/") if target.startswith("/") else
                                      posixpath.join(posixpath.dirname(source), target))
        if resolved in (".", "..") or resolved.startswith("../"):
            raise ParseFailure("UNSAFE_DOCUMENT")
        if resolved not in self.names:
            raise ParseFailure("CORRUPT_DOCUMENT")
        return resolved

    def relationships(self, root, source):
        if root.tag != q(REL, "Relationships"):
            raise ParseFailure("CORRUPT_DOCUMENT")
        result = {}
        for rel in root:
            if rel.tag != q(REL, "Relationship"):
                raise ParseFailure("CORRUPT_DOCUMENT")
            kind, identity = rel.get("Type", ""), rel.get("Id", "")
            if not identity or identity in result:
                raise ParseFailure("CORRUPT_DOCUMENT")
            mode = rel.get("TargetMode", "Internal")
            if mode == "External" and kind == R + "/hyperlink":
                # Hyperlink targets are inert relationship data. Preserve the
                # visible run text; never resolve/fetch/execute or emit the URI.
                target = rel.get("Target", "")
                if not target or "\x00" in target:
                    raise ParseFailure("CORRUPT_DOCUMENT")
                result[identity] = (kind, target)
                continue
            if mode != "Internal":
                raise ParseFailure("UNSAFE_DOCUMENT")
            if any(marker in kind.lower() for marker in ("vba", "activex", "oleobject")) or kind.endswith("/package"):
                raise ParseFailure("UNSAFE_DOCUMENT")
            result[identity] = (kind, self.target(source, rel.get("Target", "")))
        return result

    def main(self, fmt):
        if any(kind.endswith("/officeDocument") and kind != R + "/officeDocument"
               for kind, _ in self.rels.get("", {}).values()):
            raise ParseFailure("UNSUPPORTED_FORMAT")
        roots = [target for kind, target in self.rels.get("", {}).values() if kind == R + "/officeDocument"]
        if len(roots) != 1:
            raise ParseFailure("CORRUPT_DOCUMENT")
        expected = {
            "docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml",
            "pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml",
        }
        if self.types.get(roots[0]) != expected[fmt]:
            raise ParseFailure("UNSUPPORTED_FORMAT")
        return roots[0], self.xml[roots[0]]


def paragraph_text(node, namespace):
    result = []
    def visit(element):
        if element is not node and element.tag == q(namespace, "p"):
            return
        if element.tag in (q(W, "del"), q(MC, "AlternateContent")):
            return
        if element.tag == q(namespace, "t"):
            result.append(element.text or "")
        elif element.tag == q(namespace, "tab"):
            result.append("\t")
        elif element.tag in (q(namespace, "br"), q(namespace, "cr")):
            result.append("\n")
        else:
            for child in element:
                visit(child)
    visit(node)
    return "".join(result)


def body_units(root, namespace, output, location):
    paragraph, table = 0, 0
    def members(node, tag, context):
        """Unwrap content controls without entering nested tables/rows/cells."""
        for child in node:
            if child.tag == tag:
                yield child
            elif child.tag == q(MC, "AlternateContent"):
                output.warn("UNSUPPORTED_CONTENT", context)
            elif child.tag not in {q(namespace, name) for name in ("tbl", "tr", "tc", "p")}:
                yield from members(child, tag, context)
            elif any(desc.tag == q(namespace, "p") for desc in child.iter()):
                output.warn("UNSUPPORTED_CONTENT", context)
    def visit(node, context):
        nonlocal paragraph, table
        if node.tag == q(W, "del"):
            return
        if node.tag in (q(MC, "AlternateContent"), q(W, "altChunk"),
                        "{http://schemas.openxmlformats.org/drawingml/2006/chart}chart",
                        "{http://schemas.openxmlformats.org/drawingml/2006/diagram}relIds") or node.tag.startswith(
                "{http://schemas.openxmlformats.org/officeDocument/2006/math}"):
            output.warn("UNSUPPORTED_CONTENT", context)
            return
        if node.tag == q(namespace, "tbl"):
            table += 1
            current = table
            for row_index, row in enumerate(members(node, q(namespace, "tr"), context), 1):
                for cell_index, cell in enumerate(members(row, q(namespace, "tc"), context), 1):
                    visit(cell, {**context, "table": current, "row": row_index, "cell": cell_index})
            return
        if node.tag == q(namespace, "p"):
            paragraph += 1
            output.add(paragraph_text(node, namespace), {**context, "paragraph": paragraph})
        for child in node:
            visit(child, context)
    visit(root, location)
    return paragraph


def office_text(data, fmt, limits):
    package = Package(data, limits)
    try:
        part, root = package.main(fmt)
        output = Collector(fmt, limits, "document_body" if fmt == "docx" else "slide_body")
        if fmt == "docx":
            body = root.find(q(W, "body"))
            if root.tag != q(W, "document") or body is None:
                raise ParseFailure("UNSUPPORTED_FORMAT")
            count = body_units(body, W, output, {"kind": "paragraph", "part": part})
            output.result["metrics"]["paragraphs"] = count
        else:
            if root.tag != q(P, "presentation"):
                raise ParseFailure("UNSUPPORTED_FORMAT")
            slides = root.find(q(P, "sldIdLst"))
            if slides is None:
                # The slide list is optional in a valid empty presentation.
                raise ParseFailure("NO_EXTRACTABLE_TEXT")
            if len(slides) > limits.max_slides:
                raise ParseFailure("LIMIT_EXCEEDED")
            seen = set()
            for number, slide in enumerate(slides, 1):
                identity = slide.get(q(R, "id"))
                if slide.tag != q(P, "sldId") or not identity or identity in seen:
                    raise ParseFailure("CORRUPT_DOCUMENT")
                seen.add(identity)
                kind, target = package.rels.get(part, {}).get(identity, ("", ""))
                if kind != R + "/slide" or target not in package.xml:
                    raise ParseFailure("CORRUPT_DOCUMENT")
                slide_root = package.xml[target]
                tree = slide_root.find(q(P, "cSld") + "/" + q(P, "spTree"))
                if slide_root.tag != q(P, "sld") or tree is None:
                    raise ParseFailure("CORRUPT_DOCUMENT")
                before = len(output.result["units"])
                body_units(tree, A, output, {"kind": "slide", "slide": number, "part": target,
                                             "hidden": slide_root.get("show") == "0"})
                if len(output.result["units"]) == before:
                    output.warn("NO_TEXT_ON_SLIDE", {"slide": number})
            output.result["metrics"]["slides"] = len(slides)
        return output.finish()
    finally:
        package.zip.close()
