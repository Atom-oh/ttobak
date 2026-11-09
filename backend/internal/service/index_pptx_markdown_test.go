package service

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"math/rand"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

const (
	pptxNS = `xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" ` +
		`xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
	pptxRelNS  = `http://schemas.openxmlformats.org/officeDocument/2006/relationships`
	pptxPkgRel = `http://schemas.openxmlformats.org/package/2006/relationships`
)

type pptxEntry struct {
	name, body string
	raw        func(*zip.Writer) // optional custom writer for hostile entries
}

func buildPPTX(t *testing.T, entries []pptxEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		if entry.raw != nil {
			entry.raw(writer)
			continue
		}
		part, err := writer.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func pptxShape(placeholder string, paragraphs ...string) string {
	ph := ""
	if placeholder != "" {
		ph = `<p:ph type="` + placeholder + `"/>`
	}
	body := ""
	for _, paragraph := range paragraphs {
		level := ""
		if strings.HasPrefix(paragraph, ">") {
			level, paragraph = `<a:pPr lvl="1"/>`, strings.TrimPrefix(paragraph, ">")
		}
		body += `<a:p>` + level + `<a:r><a:t>` + paragraph + `</a:t></a:r></a:p>`
	}
	return `<p:sp><p:nvSpPr><p:cNvPr id="2" name="s"/><p:cNvSpPr/><p:nvPr>` + ph +
		`</p:nvPr></p:nvSpPr><p:txBody><a:bodyPr/>` + body + `</p:txBody></p:sp>`
}

func pptxSlideXML(shapes ...string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><p:sld ` + pptxNS +
		`><p:cSld><p:spTree>` + strings.Join(shapes, "") + `</p:spTree></p:cSld></p:sld>`
}

func pptxTableXML(rows ...[]string) string {
	body := ""
	for _, row := range rows {
		body += "<a:tr>"
		for _, cell := range row {
			body += `<a:tc><a:txBody><a:p><a:r><a:t>` + cell + `</a:t></a:r></a:p></a:txBody></a:tc>`
		}
		body += "</a:tr>"
	}
	return `<p:graphicFrame><a:graphic><a:graphicData><a:tbl>` + body + `</a:tbl></a:graphicData></a:graphic></p:graphicFrame>`
}

// pptxDeck lists slide parts in deck order; notes maps slide part to notes XML.
func pptxDeck(slides []string, bodies map[string]string, notes map[string]string, extra ...pptxEntry) []pptxEntry {
	ids, rels := "", ""
	for i, slide := range slides {
		ids += fmt.Sprintf(`<p:sldId id="%d" r:id="rId%d"/>`, 256+i, i+10)
		rels += fmt.Sprintf(`<Relationship Id="rId%d" Type="%s/slide" Target="%s"/>`, i+10, pptxRelNS, strings.TrimPrefix(slide, "ppt/"))
	}
	entries := []pptxEntry{
		{name: "[Content_Types].xml", body: `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`},
		{name: "ppt/presentation.xml", body: `<?xml version="1.0"?><p:presentation ` + pptxNS + `><p:sldIdLst>` + ids + `</p:sldIdLst></p:presentation>`},
		{name: "ppt/_rels/presentation.xml.rels", body: `<?xml version="1.0"?><Relationships xmlns="` + pptxPkgRel + `">` + rels + `</Relationships>`},
	}
	for _, slide := range slides {
		entries = append(entries, pptxEntry{name: slide, body: bodies[slide]})
		name := strings.TrimPrefix(slide, "ppt/slides/")
		slideRels := `<Relationship Id="rId1" Type="` + pptxRelNS + `/hyperlink" Target="https://example.invalid/x" TargetMode="External"/>`
		if note, ok := notes[slide]; ok {
			notesName := "notes" + name
			slideRels += `<Relationship Id="rId2" Type="` + pptxRelNS + `/notesSlide" Target="../notesSlides/` + notesName + `"/>`
			entries = append(entries, pptxEntry{name: "ppt/notesSlides/" + notesName, body: note})
		}
		entries = append(entries, pptxEntry{name: "ppt/slides/_rels/" + name + ".rels",
			body: `<?xml version="1.0"?><Relationships xmlns="` + pptxPkgRel + `">` + slideRels + `</Relationships>`})
	}
	return append(entries, extra...)
}

func samplePPTX(t *testing.T, title string) []byte {
	t.Helper()
	notes := `<?xml version="1.0"?><p:notes ` + pptxNS + `><p:cSld><p:spTree>` +
		pptxShape("sldImg") + pptxShape("body", "Mention the Q3 budget") + pptxShape("sldNum", "2") +
		`</p:spTree></p:cSld></p:notes>`
	// Deck order intentionally differs from archive part names.
	return buildPPTX(t, pptxDeck([]string{"ppt/slides/slide2.xml", "ppt/slides/slide1.xml"}, map[string]string{
		"ppt/slides/slide2.xml": pptxSlideXML(pptxShape("ctrTitle", title), pptxShape("subTitle", "Kickoff &amp; plan")),
		"ppt/slides/slide1.xml": pptxSlideXML(pptxShape("title", "Roadmap"),
			pptxShape("", "Phase 1", ">Detail | a", "A\tB"), pptxTableXML([]string{"Owner", "Due"}, []string{"Kim", "10/1"}),
			pptxShape("sldNum", "7"), pptxShape("ftr", "Confidential")),
	}, map[string]string{"ppt/slides/slide1.xml": notes}))
}

const samplePPTXMarkdown = `# Deck

## Slide 1: 회의 계획

- Kickoff & plan

## Slide 2: Roadmap

- Phase 1
  - Detail | a
- A B

| Owner | Due |
| --- | --- |
| Kim | 10/1 |

### Speaker notes

- Mention the Q3 budget
`

func randomText(n int) string {
	random := rand.New(rand.NewSource(7))
	letters := []byte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	out := make([]byte, n)
	for i := range out {
		out[i] = letters[random.Intn(len(letters))]
	}
	return string(out)
}

func TestPPTXToMarkdown(t *testing.T) {
	slide := func(shapes ...string) map[string]string {
		return map[string]string{"ppt/slides/slide1.xml": pptxSlideXML(shapes...)}
	}
	one := []string{"ppt/slides/slide1.xml"}
	cfb := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 504)...)
	// A deflated stream whose header understates its decompressed length.
	lying := func(w *zip.Writer) {
		payload := pptxSlideXML(pptxShape("", strings.Repeat("x", 2<<20)))
		var compressed bytes.Buffer
		deflate, _ := flate.NewWriter(&compressed, flate.BestCompression)
		deflate.Write([]byte(payload))
		deflate.Close()
		part, err := w.CreateRaw(&zip.FileHeader{Name: "ppt/slides/slide1.xml", Method: zip.Deflate,
			CRC32: crc32.ChecksumIEEE([]byte(payload)), CompressedSize64: uint64(compressed.Len()), UncompressedSize64: 4096})
		if err != nil {
			panic(err)
		}
		part.Write(compressed.Bytes())
	}
	lyingDeck := pptxDeck(nil, nil, nil)
	lyingDeck[1].body = strings.Replace(lyingDeck[1].body, "<p:sldIdLst>", `<p:sldIdLst><p:sldId id="256" r:id="rId9"/>`, 1)
	lyingDeck[2].body = strings.Replace(lyingDeck[2].body, "</Relationships>",
		`<Relationship Id="rId9" Type="`+pptxRelNS+`/slide" Target="slides/slide1.xml"/></Relationships>`, 1)
	lyingDeck = append(lyingDeck, pptxEntry{raw: lying})

	manySlides := []string{}
	manyBodies := map[string]string{}
	for i := 0; i <= pptxMaxSlides; i++ {
		name := fmt.Sprintf("ppt/slides/slide%d.xml", i+1)
		manySlides = append(manySlides, name)
		manyBodies[name] = pptxSlideXML(pptxShape("title", "t"))
	}
	manyEntries := pptxDeck(one, slide(pptxShape("title", "t")), nil)
	for i := 0; i < pptxMaxEntries; i++ {
		manyEntries = append(manyEntries, pptxEntry{name: fmt.Sprintf("ppt/media/image%d.png", i), body: "x"})
	}
	bigParagraphs := []string{}
	for i := 0; i < 5000; i++ {
		bigParagraphs = append(bigParagraphs, randomText(1000))
	}

	cases := []struct {
		name string
		data []byte
		code string
		want string
	}{
		{name: "slides, notes, tables and deck order", data: samplePPTX(t, "회의 계획"), want: samplePPTXMarkdown},
		{name: "not a zip", data: []byte("plain text"), code: PPTXErrMalformed},
		{name: "OLE container", data: cfb, code: PPTXErrEncrypted},
		{name: "missing presentation", data: buildPPTX(t, []pptxEntry{{name: "a.xml", body: "<a/>"}}), code: PPTXErrMalformed},
		{name: "malformed slide XML", data: buildPPTX(t, pptxDeck(one, map[string]string{"ppt/slides/slide1.xml": "<p:sld><p:cSld>"}, nil)), code: PPTXErrMalformed},
		{name: "DTD entity", data: buildPPTX(t, pptxDeck(one, map[string]string{"ppt/slides/slide1.xml": `<!DOCTYPE x [<!ENTITY e "boom">]><p:sld/>`}, nil)), code: PPTXErrMalformed},
		{name: "relationship escapes archive", data: buildPPTX(t, func() []pptxEntry {
			entries := pptxDeck(one, slide(pptxShape("title", "t")), nil)
			entries[2].body = strings.Replace(entries[2].body, `Target="slides/slide1.xml"`, `Target="../../etc/passwd"`, 1)
			return entries
		}()), code: PPTXErrMalformed},
		{name: "duplicate case-folded part", data: buildPPTX(t, pptxDeck(one, slide(pptxShape("title", "t")), nil,
			pptxEntry{name: "PPT/presentation.xml", body: "<x/>"})), code: PPTXErrMalformed},
		{name: "high-ratio part", data: buildPPTX(t, pptxDeck(one, slide(pptxShape("", strings.Repeat(" ", 3<<20))), nil)), code: PPTXErrExpansion},
		{name: "oversized part", data: buildPPTX(t, pptxDeck(one, slide(pptxShape("", randomText(pptxMaxPartBytes+1))), nil)), code: PPTXErrExpansion},
		{name: "understated decompressed size", data: buildPPTX(t, lyingDeck), code: PPTXErrExpansion},
		{name: "too many entries", data: buildPPTX(t, manyEntries), code: PPTXErrTooManyParts},
		{name: "too many slides", data: buildPPTX(t, pptxDeck(manySlides, manyBodies, nil)), code: PPTXErrTooManySlides},
		{name: "output too large", data: buildPPTX(t, pptxDeck(one, slide(pptxShape("", bigParagraphs...)), nil)), code: PPTXErrOutputLimit},
		{name: "no text", data: buildPPTX(t, pptxDeck(one, slide(pptxShape("sldNum", "1")), nil)), code: PPTXErrNoText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PPTXToMarkdown(tc.data, "Deck")
			code, _ := PPTXErrorCode(err)
			if tc.code != "" {
				if code != tc.code || got != nil {
					t.Fatalf("code = %q (%v), want %q", code, err, tc.code)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("markdown mismatch:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestKnowledgeProjectionNames(t *testing.T) {
	for _, tc := range []struct{ key, name, title string }{
		{"kb/owner/1784866428072_gpu-obs.pptx", "document.md", "gpu-obs"},
		{"kb/owner/1784866428072_Deck.PPTX", "document.md", "Deck"},
		{"kb/owner/report.pdf", "document.pdf", "report"},
		{"shared/a/b.docx", "document.docx", "b"},
		{"kb/owner/v2_notes.xlsx", "document.xlsx", "v2_notes"},
	} {
		if got := knowledgeProjectionName(tc.key); got != tc.name || !model.KnowledgeProjectionKey(
			"manual-kb/v1/owner/"+strings.Repeat("a", 64)+"/"+strings.Repeat("b", 64)+"/00000000-0000-0000-0000-000000000000/"+got) {
			t.Fatalf("%s: projection name %q", tc.key, got)
		}
		if got := knowledgeTitle(tc.key); got != tc.title {
			t.Fatalf("%s: title %q", tc.key, got)
		}
	}
}

func knowledgeProjection(t *testing.T, repo *indexMemory, store *knowledgeFixture, key model.IndexResource) (string, map[string]interface{}) {
	t.Helper()
	job := repo.jobs[key.Hash()]
	if job == nil {
		t.Fatal("source was not discovered")
	}
	var document string
	var meta map[string]interface{}
	for _, staged := range job.Keys {
		if strings.HasSuffix(staged, ".metadata.json") {
			var envelope struct {
				MetadataAttributes map[string]interface{} `json:"metadataAttributes"`
			}
			if err := json.Unmarshal(store.kb[staged], &envelope); err != nil {
				t.Fatal(err)
			}
			meta = envelope.MetadataAttributes
			continue
		}
		document = staged
	}
	return document, meta
}

func TestKnowledgePPTXPublishesRevisionBoundMarkdown(t *testing.T) {
	service, repo, store, sync, _ := newKnowledgeTest()
	key := store.original("kb/owner/1784866428072_Deck.pptx", string(samplePPTX(t, "회의 계획")), "v1")
	if _, err := service.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := repo.jobs[key.Hash()]
	document, meta := knowledgeProjection(t, repo, store, key)
	if first.State != model.IndexWaitingSync || !strings.HasSuffix(document, "/"+first.Revision+"/"+first.RunID+"/document.md") ||
		!model.KnowledgeProjectionKey(document) {
		t.Fatalf("pptx was not staged as an immutable Markdown projection: %+v %s", first, document)
	}
	if string(store.kb[document]) != samplePPTXMarkdown || store.contentTypes[document] != pptxMarkdownType {
		t.Fatalf("projection body/type mismatch: %q %q", store.kb[document], store.contentTypes[document])
	}
	if meta["sourceProjection"] != PPTXMarkdownProjection || meta["sourceRevision"] != first.Revision ||
		meta["sourceETag"] != store.headers[key.SourceKey].ETag || meta["ownerId"] != "owner" {
		t.Fatalf("projection metadata is not bound to the source: %+v", meta)
	}
	finishIndex(t, service, sync)
	if repo.jobs[key.Hash()].State != model.IndexIndexed {
		t.Fatalf("pptx projection not indexed: %+v", repo.jobs[key.Hash()])
	}

	// Same key, new bytes (new ETag): the next revision regenerates Markdown
	// under a new immutable prefix and removes the obsolete projection.
	store.original(key.SourceKey, string(samplePPTX(t, "Revised plan")), "v2")
	for i := 0; i < 3 && repo.jobs[key.Hash()].Revision == first.Revision; i++ {
		if _, err := service.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	second := repo.jobs[key.Hash()]
	next, _ := knowledgeProjection(t, repo, store, key)
	if second.Revision == first.Revision || next == document || !strings.Contains(next, "/"+second.Revision+"/") {
		t.Fatalf("ETag change did not produce a new revision-bound projection: %+v", second)
	}
	if !strings.Contains(string(store.kb[next]), "## Slide 1: Revised plan") {
		t.Fatalf("projection not regenerated from new bytes: %q", store.kb[next])
	}
	finishIndex(t, service, sync)
	if _, exists := store.kb[document]; exists || repo.jobs[key.Hash()].State != model.IndexIndexed {
		t.Fatalf("obsolete projection survived or new revision not indexed: %+v", repo.jobs[key.Hash()])
	}
}

func TestKnowledgePresentationFailuresFailClosed(t *testing.T) {
	for _, tc := range []struct{ key, body, code string }{
		{"kb/owner/legacy.ppt", "\xD0\xCF\x11\xE0 legacy binary", LegacyPPTUnsupported},
		{"kb/owner/broken.pptx", "PK\x03\x04 not really a zip", PPTXErrMalformed},
		{"shared/team/encrypted.pptx", "\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1 encrypted", PPTXErrEncrypted},
	} {
		t.Run(tc.key, func(t *testing.T) {
			service, repo, store, _, _ := newKnowledgeTest()
			key := store.original(tc.key, tc.body, "v1")
			if _, err := service.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			job := repo.jobs[key.Hash()]
			if job == nil || job.State != model.IndexFailed || job.ErrorCode != tc.code || len(job.Keys) != 0 {
				t.Fatalf("want FAILED %s without projections, got %+v", tc.code, job)
			}
			for staged := range store.kb {
				if strings.Contains(staged, key.ID) {
					t.Fatalf("failed presentation staged %s", staged)
				}
			}
			if string(store.originals[tc.key]) != tc.body {
				t.Fatal("original modified")
			}
			status, err := service.Status(context.Background(), key)
			if err != nil || status.State != model.IndexFailed || status.ErrorCode != tc.code {
				t.Fatalf("status lost the precise failure: %+v %v", status, err)
			}
		})
	}
}
