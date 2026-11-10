package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestResummaryGenerationUsesSavedNotesWithoutAudioOrStorageWrites(t *testing.T) {
	m := &model.Meeting{MeetingID: "m", UserID: "owner", Notes: "사용자 메모입니다.", Content: "직접 수정한 저장 요약입니다."}
	request, content, writes, err := invokeNoteSourceFixture(t, m, `{"content":[{"type":"text","text":"메모 요약 [TS:12] [old](transcript://old)"}],"stop_reason":"end_turn"}`,
		func(s *BedrockService) (string, error) { return s.GenerateResummary(context.Background(), m, nil) })
	if err != nil {
		t.Fatal(err)
	}
	if writes != 0 || strings.Contains(content, "transcript://") || strings.Contains(content, "[TS:") {
		t.Fatalf("writes=%d result=%s", writes, content)
	}
	prompt := request.Messages[0].Content[0].Text
	if !strings.Contains(prompt, m.Notes) || !strings.Contains(prompt, m.Content) || !strings.Contains(request.System, "현재 녹취 근거가 없습니다") {
		t.Fatal("missing saved-note source contract")
	}
}

func TestResummaryRejectsIncompleteModelAndEmptySources(t *testing.T) {
	m := &model.Meeting{MeetingID: "meeting", UserID: "owner", Notes: "saved note"}
	_, _, writes, err := invokeNoteSourceFixture(t, m, `{"content":[{"type":"text","text":"truncated"}],"stop_reason":"max_tokens"}`,
		func(s *BedrockService) (string, error) { return s.GenerateResummary(context.Background(), m, nil) })
	if err == nil || writes != 0 {
		t.Fatal("incomplete generation succeeded")
	}
	s := &BedrockService{}
	if _, err := s.GenerateResummary(context.Background(), &model.Meeting{}, nil); !errors.Is(err, ErrResummaryNoSource) {
		t.Fatal("empty source reached model")
	}
}

func TestGenerateEnglishNoteUsesEnglishDirectiveWithoutKoreanNote(t *testing.T) {
	m := &model.Meeting{MeetingID: "m", UserID: "owner", Notes: "synthetic memo", Content: "KOREAN-NOTE-BODY", NoteLanguage: model.NoteLanguageKorean}
	request, content, writes, err := invokeNoteSourceFixture(t, m, `{"content":[{"type":"text","text":"# Meeting Notes\n\nSynthetic overview sentence."}],"stop_reason":"end_turn"}`,
		func(s *BedrockService) (string, error) { return s.GenerateEnglishNote(context.Background(), m, nil) })
	if err != nil || writes != 0 || content == "" {
		t.Fatalf("content=%q writes=%d err=%v", content, writes, err)
	}
	if !strings.Contains(request.System, noteLanguageDirective(model.NoteLanguageEnglish)) || strings.Contains(request.System, noteLanguageDirective(model.NoteLanguageKorean)) {
		t.Fatal("English note prompt missing the English directive")
	}
	if strings.Contains(request.Messages[0].Content[0].Text, "KOREAN-NOTE-BODY") || strings.Contains(request.System, "KOREAN-NOTE-BODY") {
		t.Fatal("English note prompt contains the Korean note")
	}
	if !strings.Contains(request.Messages[0].Content[0].Text, m.Notes) {
		t.Fatal("English note prompt lost saved notes")
	}
	s := &BedrockService{}
	if _, err := s.GenerateEnglishNote(context.Background(), &model.Meeting{Content: "KOREAN-ONLY"}, nil); !errors.Is(err, ErrResummaryNoSource) {
		t.Fatal("Korean note alone reached the model")
	}
}
