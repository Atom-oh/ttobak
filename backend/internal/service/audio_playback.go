package service

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/ttobak/backend/internal/audioplayback"
)

// playbackLookupTimeout bounds both HEADs of the optional copy lookup so a
// stalled S3 connection falls back to the original instead of consuming the
// API request deadline.
var playbackLookupTimeout = 2 * time.Second

// PlaybackAudioKey returns the ADR-062 seekable copy of sourceKey when it
// exists and is bound to the source's current ETag and VersionId; otherwise
// sourceKey itself. Lookup failures fall back to the original, never fail
// the request: the copy is an optimization, not the record of the audio.
func (s *UploadService) PlaybackAudioKey(ctx context.Context, sourceKey string) string {
	playbackKey, ok := audioplayback.KeyFor(sourceKey)
	if !ok || s.s3Client == nil {
		return sourceKey
	}
	ctx, cancel := context.WithTimeout(ctx, playbackLookupTimeout)
	defer cancel()
	source, err := s.s3Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucketName), Key: aws.String(sourceKey)})
	if err != nil || source == nil {
		log.Printf("warn: audio playback source HEAD failed for %s: %v", sourceKey, err)
		return sourceKey
	}
	copyHead, err := s.s3Client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucketName), Key: aws.String(playbackKey)})
	if err != nil || copyHead == nil {
		var notFound *s3types.NotFound
		if !errors.As(err, &notFound) {
			log.Printf("warn: audio playback copy HEAD failed for %s: %v", playbackKey, err)
		}
		return sourceKey
	}
	if !audioplayback.MatchesSource(copyHead.Metadata, aws.ToString(source.ETag), aws.ToString(source.VersionId)) {
		return sourceKey
	}
	return playbackKey
}

// GenerateAudioPlaybackURL signs the playback key selected for sourceKey.
func (s *UploadService) GenerateAudioPlaybackURL(ctx context.Context, sourceKey string) (string, error) {
	return s.GeneratePresignedDownloadURL(ctx, s.PlaybackAudioKey(ctx, sourceKey))
}
