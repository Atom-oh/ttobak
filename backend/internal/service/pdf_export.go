package service

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/signintech/gopdf"
)

//go:embed pdfassets/node_modules/pretendard/dist/public/static/alternative/Pretendard-Regular.ttf
var pdfExportFont []byte

//go:embed pdfassets/node_modules/pretendard/dist/LICENSE.txt
var pdfExportFontLicense []byte

var (
	ErrPDFExportLimit = errors.New("PDF export exceeds its size or page limit")
	ErrPDFExportGlyph = errors.New("PDF export contains unsupported characters")
)

const (
	maxPDFSourceBytes = 2 * 1024 * 1024
	maxPDFPages       = 200
	maxPDFBytes       = 8 * 1024 * 1024
	pdfFontSHA256     = "6d0af5258997aec7354a6e340fc2325ba321c410ca48b3af858c8c3d6e92a324"
	pdfLicenseSHA256  = "b04538c9abec39a3db75108cf0af0fd9c77032fe8aa2cf38345b4d250e98e38e"
)

func validatePDFAssets(font, license []byte) error {
	fontDigest, licenseDigest := sha256.Sum256(font), sha256.Sum256(license)
	if hex.EncodeToString(fontDigest[:]) != pdfFontSHA256 ||
		hex.EncodeToString(licenseDigest[:]) != pdfLicenseSHA256 {
		return errors.New("PDF font asset verification failed")
	}
	return nil
}

// RenderMeetingPDF embeds a licensed Korean font and preserves all source text.
// Unsupported glyphs and excessive documents fail explicitly instead of
// producing a successful, silently incomplete export.
func RenderMeetingPDF(ctx context.Context, content string) ([]byte, error) {
	if len(content) > maxPDFSourceBytes || !utf8.ValidString(content) {
		return nil, ErrPDFExportLimit
	}
	if err := validatePDFAssets(pdfExportFont, pdfExportFontLicense); err != nil {
		return nil, err
	}
	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4})
	if err := pdf.AddTTFFontData("Pretendard", pdfExportFont); err != nil {
		return nil, errors.New("PDF font initialization failed")
	}
	if err := pdf.SetFont("Pretendard", "", 11); err != nil {
		return nil, errors.New("PDF font selection failed")
	}
	content = strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	content = strings.ReplaceAll(content, "\t", "    ")
	glyphs := map[rune]bool{}
	for _, r := range content {
		if r == '\n' || glyphs[r] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ok, err := pdf.IsCurrFontContainGlyph(r)
		if err != nil || !ok {
			return nil, ErrPDFExportGlyph
		}
		glyphs[r] = true
	}
	const margin, lineHeight = 44.0, 17.0
	pages, y := 0, margin
	newPage := func() error {
		pages++
		if pages > maxPDFPages {
			return ErrPDFExportLimit
		}
		pdf.AddPage()
		y = margin
		pdf.SetXY(margin, y)
		return nil
	}
	if err := newPage(); err != nil {
		return nil, err
	}
	for _, paragraph := range strings.Split(content, "\n") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lines := []string{""}
		if paragraph != "" {
			var err error
			lines, err = pdf.SplitText(paragraph, gopdf.PageSizeA4.W-2*margin)
			if err != nil {
				return nil, errors.New("PDF text layout failed")
			}
		}
		for _, line := range lines {
			if y+lineHeight > gopdf.PageSizeA4.H-margin {
				if err := newPage(); err != nil {
					return nil, err
				}
			}
			pdf.SetXY(margin, y)
			if line != "" {
				if err := pdf.Cell(nil, line); err != nil {
					return nil, errors.New("PDF text rendering failed")
				}
			}
			y += lineHeight
		}
	}
	data, err := pdf.GetBytesPdfReturnErr()
	if err != nil {
		return nil, errors.New("PDF serialization failed")
	}
	if len(data) > maxPDFBytes {
		return nil, ErrPDFExportLimit
	}
	return data, nil
}
