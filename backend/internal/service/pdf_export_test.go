package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPDFExportVerifiesEmbeddedFontAndLicense(t *testing.T) {
	if err := validatePDFAssets(pdfExportFont, pdfExportFontLicense); err != nil {
		t.Fatal(err)
	}
	for _, assets := range [][2][]byte{
		{nil, pdfExportFontLicense},
		{pdfExportFont, nil},
		{[]byte("corrupt font"), pdfExportFontLicense},
		{pdfExportFont, []byte("corrupt license")},
	} {
		if err := validatePDFAssets(assets[0], assets[1]); err == nil {
			t.Fatal("unverified embedded assets accepted")
		}
	}
}

func TestPDFExportEmbedsKoreanTextAndPaginates(t *testing.T) {
	text := "회의 제목: 한국어 검증\n결정: 비용 1,200,000원, 지연 30ms\n담당자: 민수\n"
	data, err := RenderMeetingPDF(context.Background(), strings.Repeat(text, 200))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) || !bytes.Contains(data, []byte("/ToUnicode")) ||
		bytes.Count(data, []byte("/Type /Page\n")) < 2 {
		t.Fatal("PDF did not contain embedded Unicode text and multiple pages")
	}
	// Optional operator/test parser output; the fixture contains no customer data.
	if target := os.Getenv("TTOBAK_PDF_TEST_OUTPUT"); target != "" {
		if !filepath.IsAbs(target) {
			t.Fatal("PDF test output needs an absolute path")
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPDFExportFailsExplicitlyRatherThanDroppingContent(t *testing.T) {
	for _, text := range []string{strings.Repeat("a", maxPDFSourceBytes+1), string([]byte{0xff})} {
		if _, err := RenderMeetingPDF(context.Background(), text); !errors.Is(err, ErrPDFExportLimit) {
			t.Fatal("oversized or malformed text accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RenderMeetingPDF(ctx, "회의"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled export continued")
	}
	if _, err := RenderMeetingPDF(context.Background(), "지원하지 않는 글자: \U0010ffff"); !errors.Is(err, ErrPDFExportGlyph) {
		t.Fatal("unsupported glyph silently removed")
	}
	if _, err := RenderMeetingPDF(context.Background(), strings.Repeat("회의\n", 10000)); !errors.Is(err, ErrPDFExportLimit) {
		t.Fatal("unbounded page output accepted")
	}
}
