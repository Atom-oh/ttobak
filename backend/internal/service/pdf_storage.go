package service

import (
	"bytes"
	"context"
	"errors"
	"mime"
	"strings"
	"time"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

// StorePDFExport uses the existing private files prefix and download signer.
// A fresh key prevents one export from overwriting another user's or run's file.
func (s *UploadService) StorePDFExport(ctx context.Context, userID, meetingID, filename string, data []byte) (string, error) {
	if !validPDFIdentifier(userID) || !validPDFIdentifier(meetingID) ||
		!strings.HasPrefix(string(data), "%PDF-") || len(data) > maxPDFBytes ||
		s.bucketName == "" || s.s3Client == nil {
		return "", errors.New("invalid PDF export")
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	if disposition == "" {
		return "", errors.New("invalid PDF filename")
	}
	key := "files/" + userID + "/" + meetingID + "/export_" + uuid.NewString() + ".pdf"
	_, err := s.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucketName), Key: aws.String(key), Body: bytes.NewReader(data),
		ContentType: aws.String("application/pdf"), ContentDisposition: aws.String(disposition),
		CacheControl: aws.String("private, no-store"),
		Tagging:      aws.String("ttobak-artifact=meeting-pdf-export"),
	})
	if err != nil {
		return "", errors.New("PDF export storage failed")
	}
	url, err := s.GeneratePresignedDownloadURLWithTTL(ctx, key, 5*time.Minute)
	if err != nil {
		_, cleanupErr := s.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucketName), Key: aws.String(key),
		})
		if cleanupErr != nil {
			return "", errors.New("PDF download signing and cleanup failed")
		}
		return "", errors.New("PDF download signing failed")
	}
	return url, nil
}

func validPDFIdentifier(value string) bool {
	return value != "" && len(value) <= 128 && value != "." && value != ".." &&
		!strings.ContainsAny(value, "/\\%?#") && !strings.ContainsFunc(value, unicode.IsControl)
}
