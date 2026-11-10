package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateInterpretation(t *testing.T) {
	tests := []struct {
		name   string
		source string
		output string
		src    string
		tgt    string
		ok     bool
	}{
		{"plain english", "다음 주에 배포합니다.", "We deploy next week.", "ko", "en", true},
		{"plain korean", "We deploy next week.", "다음 주에 배포합니다.", "en", "ko", true},
		{"korean with product names", "Lambda timeout is set in CDK.", "Lambda 타임아웃은 CDK에서 설정합니다.", "en", "ko", true},
		{"latin-only source kept as spoken", "AWS Lambda", "AWS Lambda", "en", "ko", true},
		{"latin-only source kept case-insensitively", "aws lambda, ok?", "AWS Lambda OK", "auto", "ko", true},
		{"numbers only", "3, 2, 1", "3, 2, 1", "en", "ko", true},
		{"here is as speech", "보고서 여기 있습니다.", "Here is the report.", "ko", "en", true},
		{"english prefix label", "안녕하세요", "Translation: Hello", "ko", "en", false},
		{"english prefix any case", "안녕하세요", "  translated text: Hello", "ko", "en", false},
		{"korean prefix label", "Hello", "번역: 안녕하세요", "en", "ko", false},
		{"korean result label", "Hello", "번역 결과: 안녕하세요", "en", "ko", false},
		{"here is the translation", "안녕하세요", "Here is the translation: Hello", "ko", "en", false},
		{"here's the translated", "안녕하세요", "Here's the translated text: Hello", "ko", "en", false},
		{"json object output", "안녕하세요", `{"current_segment":"Hello"}`, "ko", "en", false},
		{"echoed key without braces", "안녕하세요", "current_segment: Hello", "ko", "en", false},
		{"echoed context key", "안녕하세요", "Hello (previous_segments ignored)", "ko", "en", false},
		{"korean target left in english", "We will deploy next week.", "We will deploy next week.", "en", "ko", false},
		{"latin source but new latin words", "AWS Lambda", "AWS Lambda function", "en", "ko", false},
		{"korean source not translated to korean target", "배포 OK", "deploy OK", "ko", "ko", false},
		{"english target left in korean", "다음 주에 배포합니다.", "다음 주에 배포합니다.", "ko", "en", false},
		{"english target with a korean name", "김민수 님이 발표합니다.", "Kim Minsu presents. (민수)", "ko", "en", false},
		{"english target tiny hangul share", "이번 릴리스는 금요일까지 AWS Lambda CDK 배포 확정입니다.", "This release, including the AWS Lambda CDK deployment, is final by Friday 금.", "ko", "en", true},
		{"output within length bound", "hi", strings.Repeat("가", 69), "en", "ko", true},
		{"output beyond length bound", "hi", strings.Repeat("가", 70), "en", "ko", false},
		{"plain japanese", "안녕하세요", "こんにちは", "ko", "ja", true},
		{"plain chinese", "안녕하세요", "大家好", "ko", "zh", true},
		{"japanese han only", "도쿄 본사", "東京本社", "ko", "ja", true},
		{"chinese with latin product names", "Lambda 함수를 CDK로 배포합니다.", "我们用 CDK 部署 Lambda 函数。", "ko", "zh", true},
		{"japanese with latin product names", "Lambda 함수를 CDK로 배포합니다.", "Lambda 関数を CDK でデプロイします。", "ko", "ja", true},
		{"chinese target acronym kept", "AWS", "AWS", "en", "zh", true},
		{"japanese target acronym kept", "AWS", "AWS", "en", "ja", true},
		{"chinese target number kept", "2026", "2026", "en", "zh", true},
		{"japanese target url kept", "https://aws.amazon.com", "https://aws.amazon.com", "en", "ja", true},
		{"chinese target echoed english", "We will deploy next week.", "We will deploy next week.", "en", "zh", false},
		{"japanese target echoed english", "We will deploy next week.", "We will deploy next week.", "en", "ja", false},
		{"chinese target english from korean source", "다음 주에 배포합니다.", "We deploy next week.", "ko", "zh", false},
		{"chinese target left in korean", "안녕하세요", "안녕하세요 여러분", "ko", "zh", false},
		{"japanese target left in korean", "안녕하세요", "안녕하세요 여러분", "ko", "ja", false},
		{"chinese target mostly korean with han", "회의 시작", "會議 시작합니다 여러분", "ko", "zh", false},
		{"english target left in chinese", "我们下周部署。", "我们下周部署。", "zh", "en", false},
		{"english target left in japanese", "来週デプロイします。", "来週デプロイします。", "ja", "en", false},
		{"english from chinese", "我们下周部署。", "We deploy next week.", "zh", "en", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInterpretation(tt.source, tt.output, tt.src, tt.tgt)
			if (err == nil) != tt.ok {
				t.Fatalf("validateInterpretation(%q -> %q) = %v, want ok=%v", tt.source, tt.output, err, tt.ok)
			}
			if err != nil && !errors.Is(err, errInvalidInterpretation) {
				t.Fatalf("error %v is not errInvalidInterpretation", err)
			}
		})
	}
}

func TestTranslateSegmentInvalidOutputFallsBack(t *testing.T) {
	mt := &fakeTextTranslator{out: "hello (mt)"}
	inv := &fakeModelInvoker{body: chatResponse("Translation: hello", "stop")}
	svc := newTestTranslateService(mt, inv, lunaID)
	got, err := svc.TranslateSegment(context.Background(), TranslateRequest{Text: "안녕", SourceLang: "ko", TargetLang: "en", Quality: TranslateQualityHigh})
	if err != nil {
		t.Fatal(err)
	}
	if got.Engine != TranslateEngineTranslate || got.Text != "hello (mt)" || got.FallbackReason != TranslateFallbackInvalidOutput {
		t.Fatalf("got %+v, want Amazon Translate with reason %q", got, TranslateFallbackInvalidOutput)
	}
	if inv.calls != 1 || mt.calls != 1 {
		t.Fatalf("calls llm=%d mt=%d", inv.calls, mt.calls)
	}
}

func TestTranslateSegmentTimeoutReason(t *testing.T) {
	mt := &fakeTextTranslator{out: "hello (mt)"}
	inv := &fakeModelInvoker{blockMs: 2 * time.Second, body: chatResponse("late", "stop")}
	svc := newTestTranslateService(mt, inv, lunaID)
	WithInterpreterTimeout(20 * time.Millisecond)(svc)
	got, err := svc.TranslateSegment(context.Background(), TranslateRequest{Text: "안녕", SourceLang: "ko", TargetLang: "en", Quality: TranslateQualityHigh})
	if err != nil {
		t.Fatal(err)
	}
	if got.Engine != TranslateEngineTranslate || got.FallbackReason != TranslateFallbackLLMError {
		t.Fatalf("got %+v", got)
	}
}

func TestInterpreterTimeoutConfiguration(t *testing.T) {
	if s := NewTranslateService(nil); s.llmTimeout != DefaultTranslateHighTimeout || DefaultTranslateHighTimeout != 6*time.Second {
		t.Fatalf("default timeout = %v", s.llmTimeout)
	}
	if s := NewTranslateService(nil, WithInterpreterTimeout(0)); s.llmTimeout != DefaultTranslateHighTimeout {
		t.Fatalf("zero must keep the default, got %v", s.llmTimeout)
	}
	tests := []struct {
		raw  string
		want time.Duration
	}{
		{"", DefaultTranslateHighTimeout},
		{"6000", 6 * time.Second},
		{" 2500 ", 2500 * time.Millisecond},
		{"500", 500 * time.Millisecond},
		{"8000", 8 * time.Second},
		{"499", DefaultTranslateHighTimeout},
		{"8001", DefaultTranslateHighTimeout},
		{"-1", DefaultTranslateHighTimeout},
		{"6s", DefaultTranslateHighTimeout},
	}
	for _, tt := range tests {
		if got := ParseTranslateHighTimeout(tt.raw); got != tt.want {
			t.Errorf("ParseTranslateHighTimeout(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}
