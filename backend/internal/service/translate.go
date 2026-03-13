package service

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/translate"
)

type TranslateService struct {
	client *translate.Client
}

func NewTranslateService(client *translate.Client) *TranslateService {
	return &TranslateService{client: client}
}

func (s *TranslateService) Translate(ctx context.Context, text, sourceLang, targetLang string) (string, error) {
	output, err := s.client.TranslateText(ctx, &translate.TranslateTextInput{
		Text:               aws.String(text),
		SourceLanguageCode: aws.String(sourceLang),
		TargetLanguageCode: aws.String(targetLang),
	})
	if err != nil {
		return "", fmt.Errorf("translate failed: %w", err)
	}
	return aws.ToString(output.TranslatedText), nil
}
