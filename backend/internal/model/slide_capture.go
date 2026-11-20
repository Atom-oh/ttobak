package model

import (
	"sort"
	"strings"
)

// SlideCaptureFilePrefix marks frames uploaded by the recording screen's
// automatic slide capture. The prefix only selects an extraction prompt and a
// summary budget; it grants no access and changes no storage rule.
const SlideCaptureFilePrefix = "slide_"

// NoSlideContent is the slide prompt's exact answer for a frame without
// presentation content. Such results are never note evidence.
const NoSlideContent = "(no slide content)"

// MaxSummarySlides bounds how many slide analyses one note uses, keeping the
// summary snapshot well under its 100-condition transaction limit.
const MaxSummarySlides = 40

// IsSlideCaptureKey reports whether an image object key names an automatic
// slide capture frame. Upload keys carry sanitizeFileName's millisecond
// prefix, so the minted base name is "{digits}_slide_{rest}".
func IsSlideCaptureKey(key string) bool {
	base := key
	if i := strings.LastIndex(key, "/"); i >= 0 {
		base = key[i+1:]
	}
	sep := strings.IndexByte(base, '_')
	if sep <= 0 {
		return false
	}
	for _, r := range base[:sep] {
		if r < '0' || r > '9' {
			return false
		}
	}
	name := base[sep+1:]
	return strings.HasPrefix(name, SlideCaptureFilePrefix) && len(name) > len(SlideCaptureFilePrefix)
}

// IsSlideAttachment reports whether an attachment row is a slide capture frame,
// before or after image processing. Only image uploads qualify: a document or
// file whose name happens to start with "slide_" keeps its normal evidence path.
func IsSlideAttachment(att *Attachment) bool {
	if att.Type == AttachTypeDocument || !strings.HasPrefix(att.OriginalKey, "images/") {
		return false
	}
	return att.Type == AttachTypeSlide || IsSlideCaptureKey(att.OriginalKey)
}

// SplitSummarySlides separates slide frames from other attachments. Slides
// without a finished analysis, or whose analysis found no slide content, carry
// no evidence and are dropped; the rest are sampled evenly in capture order
// down to at most max, so a long deck keeps coverage across the whole meeting.
// Other attachments keep their order.
func SplitSummarySlides(attachments []Attachment, max int) (others, slides []Attachment) {
	for _, att := range attachments {
		if !IsSlideAttachment(&att) {
			others = append(others, att)
			continue
		}
		content := strings.TrimSpace(att.ProcessedContent)
		if att.Status != AttachStatusDone || content == "" || content == NoSlideContent {
			continue
		}
		slides = append(slides, att)
	}
	sort.SliceStable(slides, func(i, j int) bool {
		if !slides[i].CreatedAt.Equal(slides[j].CreatedAt) {
			return slides[i].CreatedAt.Before(slides[j].CreatedAt)
		}
		return slides[i].AttachmentID < slides[j].AttachmentID
	})
	if max <= 0 {
		return others, nil
	}
	if len(slides) <= max {
		return others, slides
	}
	picked := make([]Attachment, 0, max)
	for i := 0; i < max; i++ {
		picked = append(picked, slides[i*len(slides)/max])
	}
	return others, picked
}
