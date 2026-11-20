package model

import (
	"fmt"
	"testing"
	"time"
)

func TestIsSlideCaptureKey(t *testing.T) {
	for key, want := range map[string]bool{
		"images/u/m/1700000000001_slide_001_1700000000000.jpg": true,
		"1700000000001_slide_002.jpg":                          true,
		"images/u/m/slide_001.jpg":                             false,
		"images/u/m/1700000000001_slide_":                      false,
		"images/u/m/1700000000001_screen_1.jpg":                false,
		"images/u/1_slide_x/1700000000001_photo.jpg":           false,
		"images/u/m/abc_slide_001.jpg":                         false,
		"images/u/m/1700000000001_myslide_001.jpg":             false,
	} {
		if got := IsSlideCaptureKey(key); got != want {
			t.Errorf("IsSlideCaptureKey(%q)=%v want %v", key, got, want)
		}
	}
}

func slideRow(i int, content string) Attachment {
	return Attachment{
		AttachmentID:     fmt.Sprintf("s%03d", i),
		OriginalKey:      fmt.Sprintf("images/u/m/170000000%04d_slide_%03d.jpg", i, i),
		Type:             AttachTypeSlide,
		Status:           AttachStatusDone,
		ProcessedContent: content,
		CreatedAt:        time.Unix(int64(1000+i), 0),
	}
}

func TestSplitSummarySlidesSamplesEvenlyAndDropsNonEvidence(t *testing.T) {
	photo := Attachment{AttachmentID: "p", OriginalKey: "images/u/m/1_photo.jpg", Type: AttachTypePhoto, Status: AttachStatusDone, ProcessedContent: "x"}
	pending := slideRow(500, "")
	pending.Type, pending.Status = AttachTypePhoto, AttachStatusUploaded
	empty := slideRow(501, " "+NoSlideContent+"\n")
	var rows []Attachment
	// Reverse order: selection must follow capture time, not row order.
	for i := 99; i >= 0; i-- {
		rows = append(rows, slideRow(i, "# slide"))
	}
	rows = append(rows, photo, pending, empty)

	others, slides := SplitSummarySlides(rows, 10)
	if len(others) != 1 || others[0].AttachmentID != "p" {
		t.Fatalf("others=%+v", others)
	}
	if len(slides) != 10 {
		t.Fatalf("slides=%d", len(slides))
	}
	for i, s := range slides {
		if want := fmt.Sprintf("s%03d", i*10); s.AttachmentID != want {
			t.Fatalf("slide %d = %s, want %s", i, s.AttachmentID, want)
		}
	}
	if _, none := SplitSummarySlides(rows, 0); none != nil {
		t.Fatal("zero budget kept slides")
	}
	if _, all := SplitSummarySlides(rows[:5], 10); len(all) != 5 {
		t.Fatal("small decks must keep every slide")
	}
}
