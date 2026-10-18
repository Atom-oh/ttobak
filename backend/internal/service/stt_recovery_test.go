package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	sttUser    = "owner-1"
	sttMeeting = "0b8f8a8e-1234-4abc-9def-0123456789ab"
	sttKey     = "audio/owner-1/0b8f8a8e-1234-4abc-9def-0123456789ab/1700000000000_recording.webm"
)

var sttNow = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func sttRow(mutate ...func(*model.SttRecoveryRow)) model.SttRecoveryRow {
	row := model.SttRecoveryRow{
		MeetingID: sttMeeting, UserID: sttUser, Status: model.StatusTranscribing,
		UpdatedAt: sttNow.Add(-5 * time.Hour).Format(time.RFC3339Nano), AudioKey: sttKey,
		Present: map[string]bool{"updatedAt": true, "audioKey": true, "status": true},
	}
	for _, m := range mutate {
		m(&row)
	}
	return row
}

func sttFacts(mutate ...func(*SttRecoveryFacts)) SttRecoveryFacts {
	facts := SttRecoveryFacts{AudioFound: true, AudioSize: 24_000_000, AudioModified: sttNow.Add(-6 * time.Hour)}
	for _, m := range mutate {
		m(&facts)
	}
	return facts
}

func TestClassifySttRecovery(t *testing.T) {
	cases := []struct {
		name   string
		row    model.SttRecoveryRow
		facts  SttRecoveryFacts
		action SttRecoveryAction
		reason string
		try    int
	}{
		{"done is never touched", sttRow(func(r *model.SttRecoveryRow) { r.Status = model.StatusDone }), sttFacts(), SttRecoverySkip, "status", 0},
		{"summarizing is never touched", sttRow(func(r *model.SttRecoveryRow) { r.Status = model.StatusSummarizing }), sttFacts(), SttRecoverySkip, "status", 0},
		{"legacy completed status", sttRow(func(r *model.SttRecoveryRow) { r.Status = "completed" }), sttFacts(), SttRecoverySkip, "status", 0},
		{"audio crop", sttRow(func(r *model.SttRecoveryRow) { r.AudioCrop = map[string]interface{}{"status": "queued"} }), sttFacts(), SttRecoverySkip, "audio_crop", 0},
		{"summary retry claim", sttRow(func(r *model.SttRecoveryRow) { r.SummarizeRetryClaimedAt = "x" }), sttFacts(), SttRecoverySkip, "summary_state", 0},
		{"summary retry pending", sttRow(func(r *model.SttRecoveryRow) { r.SummaryRetryPending = true }), sttFacts(), SttRecoverySkip, "summary_state", 0},
		{"retry exhausted ADR-040 state", sttRow(func(r *model.SttRecoveryRow) { r.Status, r.SummaryConflictCode = model.StatusError, "RETRY_EXHAUSTED" }), sttFacts(), SttRecoverySkip, "summary_state", 0},
		{"current AUDIO_CORRUPT mark", sttRow(markCurrent(model.SttFailureAudioCorrupt)), sttFacts(), SttRecoveryTerminal, "audio_corrupt", 0},
		{"current NO_SPEECH mark", sttRow(markCurrent(model.SttFailureNoSpeech)), sttFacts(), SttRecoveryTerminal, "no_speech", 0},
		{"current RECOVERY_EXHAUSTED mark", sttRow(markCurrent(model.SttFailureRecoveryExhausted)), sttFacts(), SttRecoveryTerminal, "recovery_exhausted", 0},
		{"terminal mark with later activity stays put", sttRow(markCurrent(model.SttFailureAudioCorrupt), func(r *model.SttRecoveryRow) { r.UpdatedAt = sttNow.Format(time.RFC3339Nano) }), sttFacts(), SttRecoverySkip, "terminal_mark_superseded", 0},
		{"checkpoint only while recording", sttRow(func(r *model.SttRecoveryRow) { r.Status, r.AudioKey = model.StatusRecording, "" }), SttRecoveryFacts{CheckpointFound: true}, SttRecoveryReportCheckpointOnly, "checkpoint_only", 0},
		{"checkpoint only in error", sttRow(func(r *model.SttRecoveryRow) { r.Status, r.AudioKey = model.StatusError, "" }), SttRecoveryFacts{CheckpointFound: true}, SttRecoveryReportCheckpointOnly, "checkpoint_only", 0},
		{"checkpoint only while transcribing", sttRow(func(r *model.SttRecoveryRow) { r.AudioKey = "" }), SttRecoveryFacts{CheckpointFound: true}, SttRecoveryReportCheckpointOnly, "checkpoint_only", 0},
		{"no audio at all", sttRow(func(r *model.SttRecoveryRow) { r.AudioKey = "" }), SttRecoveryFacts{}, SttRecoverySkip, "no_audio", 0},
		{"multipart keys", sttRow(func(r *model.SttRecoveryRow) { r.AudioKey, r.AudioKeys = "", []string{sttKey, sttKey + "2"} }), sttFacts(), SttRecoveryReportMultipart, "multipart", 0},
		{"multipart count", sttRow(func(r *model.SttRecoveryRow) { r.AudioPartCount = 3 }), sttFacts(), SttRecoveryReportMultipart, "multipart", 0},
		{"single audioKeys list", sttRow(func(r *model.SttRecoveryRow) { r.AudioKeys = []string{sttKey} }), sttFacts(), SttRecoveryReport, "audio_keys_list", 0},
		{"recording with a key", sttRow(func(r *model.SttRecoveryRow) { r.Status = model.StatusRecording }), sttFacts(), SttRecoverySkip, "recording", 0},
		{"foreign prefix", sttRow(func(r *model.SttRecoveryRow) { r.AudioKey = "audio/other/" + sttMeeting + "/x.webm" }), sttFacts(), SttRecoveryReport, "audio_key", 0},
		{"checkpoint key bound", sttRow(func(r *model.SttRecoveryRow) { r.AudioKey = "audio/owner-1/" + sttMeeting + "/checkpoint_1.webm" }), sttFacts(), SttRecoveryReport, "audio_key", 0},
		{"progress key bound", sttRow(func(r *model.SttRecoveryRow) { r.AudioKey = "audio/owner-1/" + sttMeeting + "/recording_progress.webm" }), sttFacts(), SttRecoveryReport, "audio_key", 0},
		{"realtime key bound", sttRow(func(r *model.SttRecoveryRow) { r.AudioKey = "audio/owner-1/" + sttMeeting + "/realtime_1.webm" }), sttFacts(), SttRecoveryReport, "audio_key", 0},
		{"crop result bound", sttRow(func(r *model.SttRecoveryRow) { r.AudioKey = "audio/owner-1/" + sttMeeting + "/crop_result_ab.wav" }), sttFacts(), SttRecoveryReport, "audio_key", 0},
		{"traversal", sttRow(func(r *model.SttRecoveryRow) { r.AudioKey = "audio/owner-1/" + sttMeeting + "/../x.webm" }), sttFacts(), SttRecoveryReport, "audio_key", 0},
		{"non-NFC key", sttRow(func(r *model.SttRecoveryRow) { r.AudioKey = "audio/owner-1/" + sttMeeting + "/하.webm" }), sttFacts(), SttRecoveryReport, "audio_key", 0},
		{"transcribe provider", sttRow(func(r *model.SttRecoveryRow) { r.SttProvider = "transcribe" }), sttFacts(), SttRecoveryReportUnsupported, "provider", 0},
		{"missing audio", sttRow(), SttRecoveryFacts{}, SttRecoveryReport, "audio_missing", 0},
		{"tiny audio", sttRow(), sttFacts(func(f *SttRecoveryFacts) { f.AudioSize = 1023 }), SttRecoveryReport, "audio_missing", 0},
		{"transcript newer than audio and old enough", sttRow(), sttFacts(transcriptAt(-30 * time.Minute)), SttRecoveryRedriveSummary, "transcript_unpublished", 1},
		{"transcript still fresh", sttRow(), sttFacts(transcriptAt(-10 * time.Minute)), SttRecoverySkip, "transcript_fresh", 0},
		{"summary redrive budget spent", sttRow(func(r *model.SttRecoveryRow) { r.SttSummaryRedriveAttempts, r.SttRecoveryAudioKey = 2, sttKey }), sttFacts(transcriptAt(-30 * time.Minute)), SttRecoveryReport, "summary_redrive_exhausted", 0},
		{"summary budget of replaced audio resets", sttRow(func(r *model.SttRecoveryRow) {
			r.SttSummaryRedriveAttempts, r.SttRecoveryAudioKey = 2, "audio/owner-1/"+sttMeeting+"/old.webm"
		}), sttFacts(transcriptAt(-30 * time.Minute)), SttRecoveryRedriveSummary, "transcript_unpublished", 1},
		{"rediarize restart newer than transcript is in flight", sttRow(func(r *model.SttRecoveryRow) {
			r.UpdatedAt = sttNow.Add(-10 * time.Minute).Format(time.RFC3339Nano)
		}), sttFacts(transcriptAt(-30*time.Minute), newestAudioAt(-10*time.Minute)), SttRecoverySkip, "in_flight", 0},
		{"transcribing updatedAt newer than transcript is in flight", sttRow(func(r *model.SttRecoveryRow) {
			r.UpdatedAt = sttNow.Add(-10 * time.Minute).Format(time.RFC3339Nano)
		}), sttFacts(transcriptAt(-30 * time.Minute)), SttRecoverySkip, "in_flight", 0},
		{"error row with a newer rediarize copy never republishes", sttRow(func(r *model.SttRecoveryRow) { r.Status = model.StatusError }),
			sttFacts(transcriptAt(-30*time.Minute), newestAudioAt(-20*time.Minute)), SttRecoverySkip, "in_flight", 0},
		{"recovery claim newer than transcript is in flight", sttRow(func(r *model.SttRecoveryRow) {
			r.Status, r.SttRecoveryClaimedAt = model.StatusError, sttNow.Add(-20*time.Minute).Format(time.RFC3339Nano)
		}), sttFacts(transcriptAt(-30 * time.Minute)), SttRecoverySkip, "in_flight", 0},
		{"dead restart falls back to the STT deadline", sttRow(func(r *model.SttRecoveryRow) { r.Status = model.StatusError }),
			sttFacts(transcriptAt(-5*time.Hour), newestAudioAt(-4*time.Hour)), SttRecoveryRedriveSTT, "stuck", 1},
		{"own summary claim is not an STT restart", sttRow(func(r *model.SttRecoveryRow) {
			claim := sttNow.Add(-40 * time.Minute).Format(time.RFC3339Nano)
			r.UpdatedAt, r.SttSummaryRedriveClaimedAt, r.SttSummaryRedriveAttempts, r.SttRecoveryAudioKey = claim, claim, 1, sttKey
		}), sttFacts(transcriptAt(-2 * time.Hour)), SttRecoveryRedriveSummary, "transcript_unpublished", 2},
		{"outstanding summary redrive claim is not reclaimed", sttRow(func(r *model.SttRecoveryRow) {
			claim := sttNow.Add(-5 * time.Minute).Format(time.RFC3339Nano)
			r.UpdatedAt, r.SttSummaryRedriveClaimedAt, r.SttSummaryRedriveAttempts, r.SttRecoveryAudioKey = claim, claim, 1, sttKey
		}), sttFacts(transcriptAt(-2 * time.Hour)), SttRecoverySkip, "summary_redrive_in_flight", 0},
		{"current REDRIVE_FAILED mark waits for its delay even with a transcript", sttRow(retryable(-5*time.Minute), func(r *model.SttRecoveryRow) {
			r.SttFailureCode, r.SttSummaryRedriveAttempts, r.SttRecoveryAudioKey = model.SttFailureRedriveFailed, 1, sttKey
		}), sttFacts(transcriptAt(-2 * time.Hour)), SttRecoverySkip, "in_flight", 0},
		{"transcript stamped in the restart second is superseded", sttRow(func(r *model.SttRecoveryRow) { r.Status = model.StatusError }),
			sttFacts(func(f *SttRecoveryFacts) {
				f.TranscriptFound, f.TranscriptModified = true, sttNow.Add(-3*time.Hour).Truncate(time.Second)
				f.NewestAudioModified = f.TranscriptModified.Add(400 * time.Millisecond)
			}), SttRecoveryRedriveSTT, "stuck", 1},
		{"released summary claim keeps the transcript", sttRow(retryable(-30*time.Minute), func(r *model.SttRecoveryRow) {
			r.SttFailureCode, r.SttSummaryRedriveAttempts, r.SttRecoveryAudioKey = model.SttFailureRedriveFailed, 1, sttKey
			r.SttSummaryRedriveClaimedAt = sttNow.Add(-31 * time.Minute).Format(time.RFC3339Nano)
		}), sttFacts(transcriptAt(-2 * time.Hour)), SttRecoveryRedriveSummary, "transcript_unpublished", 2},
		{"stale transcript older than audio is ignored", sttRow(), sttFacts(transcriptAt(-7 * time.Hour)), SttRecoveryRedriveSTT, "stuck", 1},
		{"stuck past deadline", sttRow(), sttFacts(), SttRecoveryRedriveSTT, "stuck", 1},
		{"error row past deadline", sttRow(func(r *model.SttRecoveryRow) { r.Status = model.StatusError }), sttFacts(), SttRecoveryRedriveSTT, "stuck", 1},
		{"second attempt same key", sttRow(func(r *model.SttRecoveryRow) { r.SttRecoveryAttempts, r.SttRecoveryAudioKey = 1, sttKey }), sttFacts(), SttRecoveryRedriveSTT, "stuck", 2},
		{"budget spent same key", sttRow(func(r *model.SttRecoveryRow) { r.SttRecoveryAttempts, r.SttRecoveryAudioKey = 2, sttKey }), sttFacts(), SttRecoveryMarkExhausted, "attempts", 0},
		{"budget resets for a new key", sttRow(func(r *model.SttRecoveryRow) {
			r.SttRecoveryAttempts, r.SttRecoveryAudioKey = 2, "audio/owner-1/"+sttMeeting+"/old.webm"
		}), sttFacts(), SttRecoveryRedriveSTT, "stuck", 1},
		{"in flight", sttRow(func(r *model.SttRecoveryRow) { r.UpdatedAt = sttNow.Add(-30 * time.Minute).Format(time.RFC3339Nano) }), sttFacts(), SttRecoverySkip, "in_flight", 0},
		{"retryable code waits for its delay", sttRow(retryable(-10 * time.Minute)), sttFacts(), SttRecoverySkip, "in_flight", 0},
		{"retryable code after its delay", sttRow(retryable(-16 * time.Minute)), sttFacts(), SttRecoveryRedriveSTT, "stuck", 1},
		{"stale retryable code never shortens a restarted run", sttRow(retryable(-2*time.Hour), func(r *model.SttRecoveryRow) {
			r.Status, r.UpdatedAt = model.StatusTranscribing, sttNow.Add(-10*time.Minute).Format(time.RFC3339Nano)
		}), sttFacts(func(f *SttRecoveryFacts) { f.AudioModified = sttNow.Add(-10 * time.Minute) }), SttRecoverySkip, "in_flight", 0},
		{"stale terminal mark on a restarted run", sttRow(func(r *model.SttRecoveryRow) {
			r.SttFailureCode, r.SttFailedAt = model.SttFailureAudioCorrupt, "2026-01-01T00:00:00Z"
		}), sttFacts(), SttRecoveryRedriveSTT, "stuck", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifySttRecovery(tc.row, tc.facts, sttNow)
			if got.Action != tc.action || got.Reason != tc.reason || got.Attempt != tc.try {
				t.Fatalf("got %+v, want action=%s reason=%s attempt=%d", got, tc.action, tc.reason, tc.try)
			}
		})
	}
}

func markCurrent(code string) func(*model.SttRecoveryRow) {
	return func(r *model.SttRecoveryRow) {
		stamp := sttNow.Add(-time.Hour).Format(time.RFC3339Nano)
		r.Status, r.SttFailureCode, r.SttFailedAt, r.UpdatedAt = model.StatusError, code, stamp, stamp
	}
}

func retryable(age time.Duration) func(*model.SttRecoveryRow) {
	return func(r *model.SttRecoveryRow) {
		stamp := sttNow.Add(age).Format(time.RFC3339Nano)
		r.Status, r.SttFailureCode, r.SttFailedAt, r.UpdatedAt = model.StatusError, model.SttFailureWorkerFailed, stamp, stamp
	}
}

func transcriptAt(age time.Duration) func(*SttRecoveryFacts) {
	return func(f *SttRecoveryFacts) { f.TranscriptFound, f.TranscriptModified = true, sttNow.Add(age) }
}

func newestAudioAt(age time.Duration) func(*SttRecoveryFacts) {
	return func(f *SttRecoveryFacts) { f.NewestAudioModified = sttNow.Add(age) }
}

func TestSttRecoveryDeadlineScalesWithAudioSize(t *testing.T) {
	audio := sttNow.Add(-10 * time.Hour)
	row := sttRow(func(r *model.SttRecoveryRow) {
		r.Status, r.UpdatedAt = model.StatusError, sttNow.Format(time.RFC3339Nano)
	})
	for _, tc := range []struct {
		bytes   int64
		minutes float64
	}{{23_800_000, 87}, {106_700_000, 128.5}, {273_000_000, 211.5}} {
		got := SttRecoveryDeadline(row, SttRecoveryFacts{AudioSize: tc.bytes, AudioModified: audio}).Sub(audio).Minutes()
		if got != tc.minutes {
			t.Fatalf("%d bytes: deadline %.1f min, want %.1f", tc.bytes, got, tc.minutes)
		}
	}
	// Error rows ignore updatedAt: a read flip may have reset it.
	if got := SttRecoveryDeadline(row, SttRecoveryFacts{AudioSize: 23_800_000, AudioModified: audio}); !got.Equal(audio.Add(87 * time.Minute)) {
		t.Fatalf("error updatedAt moved the deadline: %v", got)
	}
	transcribing := sttRow(func(r *model.SttRecoveryRow) { r.UpdatedAt = sttNow.Format(time.RFC3339Nano) })
	if got := SttRecoveryDeadline(transcribing, SttRecoveryFacts{AudioSize: 23_800_000, AudioModified: audio}); !got.Equal(sttNow.Add(87 * time.Minute)) {
		t.Fatalf("transcribing deadline must start at updatedAt: %v", got)
	}
	claimed := sttRow(func(r *model.SttRecoveryRow) {
		r.Status, r.SttRecoveryClaimedAt = model.StatusError, sttNow.Add(-time.Hour).Format(time.RFC3339Nano)
	})
	if got := SttRecoveryDeadline(claimed, SttRecoveryFacts{AudioSize: 23_800_000, AudioModified: audio}); !got.Equal(sttNow.Add(27 * time.Minute)) {
		t.Fatalf("claim must extend the deadline: %v", got)
	}
}

func TestSttFailureDeletionDue(t *testing.T) {
	marked := func(age time.Duration, mutate ...func(*model.SttRecoveryRow)) model.SttRecoveryRow {
		stamp := sttNow.Add(-age).Format(time.RFC3339Nano)
		row := sttRow(func(r *model.SttRecoveryRow) {
			r.Status, r.SttFailureCode, r.SttFailedAt, r.UpdatedAt = model.StatusError, model.SttFailureAudioCorrupt, stamp, stamp
		})
		for _, m := range mutate {
			m(&row)
		}
		return row
	}
	cases := []struct {
		name string
		row  model.SttRecoveryRow
		want bool
	}{
		{"6d23h is inside grace", marked(7*24*time.Hour - time.Hour), false},
		{"7d is due", marked(7 * 24 * time.Hour), true},
		{"no speech is due", marked(8*24*time.Hour, func(r *model.SttRecoveryRow) { r.SttFailureCode = model.SttFailureNoSpeech }), true},
		{"activity since mark", marked(8*24*time.Hour, func(r *model.SttRecoveryRow) { r.UpdatedAt = sttNow.Format(time.RFC3339Nano) }), false},
		{"exhausted is never deleted", marked(30*24*time.Hour, func(r *model.SttRecoveryRow) { r.SttFailureCode = model.SttFailureRecoveryExhausted }), false},
		{"retryable is never deleted", marked(30*24*time.Hour, func(r *model.SttRecoveryRow) { r.SttFailureCode = model.SttFailureWorkerFailed }), false},
		{"legacy meeting id", marked(30*24*time.Hour, func(r *model.SttRecoveryRow) { r.MeetingID = "meeting_1700000000" }), false},
		{"unsafe user id", marked(30*24*time.Hour, func(r *model.SttRecoveryRow) { r.UserID = "a/b" }), false},
		{"not in error", marked(30*24*time.Hour, func(r *model.SttRecoveryRow) { r.Status = model.StatusTranscribing }), false},
		{"multipart keys", marked(30*24*time.Hour, func(r *model.SttRecoveryRow) { r.AudioKeys = []string{sttKey} }), false},
		{"multipart count", marked(30*24*time.Hour, func(r *model.SttRecoveryRow) { r.AudioPartCount = 2 }), false},
		{"speaker hint is user input", marked(30*24*time.Hour, func(r *model.SttRecoveryRow) { r.DiarizationSpeakerHint = 3 }), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SttFailureDeletionDue(tc.row, sttNow); got != tc.want {
				t.Fatalf("due = %v, want %v", got, tc.want)
			}
		})
	}
}

type sttDeleteRepo struct {
	meetingRepo
	rowsErr    error
	objectsErr error
	rowCalls   int
	objCalls   int
}

func (r *sttDeleteRepo) DeleteSttFailedMeeting(context.Context, string, string, model.SttFailureMark) error {
	r.rowCalls++
	return r.rowsErr
}

func (r *sttDeleteRepo) DeleteSttFailedMeetingObjects(context.Context, string, string) (repository.SttFailedMeetingObjectsResult, error) {
	r.objCalls++
	if r.objectsErr != nil {
		return repository.SttFailedMeetingObjectsResult{Deleted: 1, Failed: 2}, r.objectsErr
	}
	return repository.SttFailedMeetingObjectsResult{Deleted: 3}, nil
}

func TestDeleteFailedRecordingOrdersRowsBeforeObjects(t *testing.T) {
	mark := model.SttFailureMark{Code: model.SttFailureAudioCorrupt, FailedAt: "2026-10-01T00:00:00Z"}
	for _, tc := range []struct {
		name               string
		repo               *sttDeleteRepo
		wantErr            error
		wantObjects        int
		wantDeleted, wantF int
	}{
		{"deletes rows then objects", &sttDeleteRepo{}, nil, 1, 3, 0},
		{"a superseded mark touches no object", &sttDeleteRepo{rowsErr: repository.ErrConditionFailed}, repository.ErrConditionFailed, 0, 0, 0},
		{"related data blocks deletion", &sttDeleteRepo{rowsErr: repository.ErrSttDeletionBlocked}, repository.ErrSttDeletionBlocked, 0, 0, 0},
		{"post-commit sweep failure still cleans objects", &sttDeleteRepo{rowsErr: repository.ErrSttDeletionPostCommit}, repository.ErrSttDeletionPostCommit, 1, 3, 0},
		{"object failures surface", &sttDeleteRepo{objectsErr: errors.New("denied")}, nil, 1, 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &MeetingService{repo: tc.repo}
			result, err := svc.DeleteFailedRecording(context.Background(), sttUser, sttMeeting, mark)
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.repo.objectsErr != nil && err == nil {
				t.Fatal("object cleanup failure was swallowed")
			}
			if tc.wantErr == nil && tc.repo.objectsErr == nil && err != nil {
				t.Fatal(err)
			}
			if tc.repo.objCalls != tc.wantObjects || result.Deleted != tc.wantDeleted || result.Failed != tc.wantF {
				t.Fatalf("objects=%d result=%+v", tc.repo.objCalls, result)
			}
		})
	}
	svc := &MeetingService{repo: &sttDeleteRepo{}}
	if _, err := svc.DeleteFailedRecording(context.Background(), sttUser, "meeting_1", mark); !errors.Is(err, repository.ErrConditionFailed) {
		t.Fatal("legacy ids must never be auto-deleted")
	}
	if _, err := svc.DeleteFailedRecording(context.Background(), sttUser, sttMeeting, model.SttFailureMark{Code: model.SttFailureWorkerFailed}); !errors.Is(err, repository.ErrConditionFailed) {
		t.Fatal("retryable codes must never be auto-deleted")
	}
}
