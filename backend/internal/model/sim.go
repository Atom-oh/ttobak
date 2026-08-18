package model

import (
	"encoding/json"
	"time"
)

// SimRun is the singleton cost/sizing simulation run for a meeting (ADR-031).
// PK: MEETING#{meetingId}, SK: SIMRUN -- singleton (one current answer per
// meeting, not a history) so the DeleteMeeting transaction only ever gains
// one extra item regardless of how many times a meeting is re-simulated,
// and so a stale "running" row can't accumulate alongside newer ones.
type SimRun struct {
	PK        string `dynamodbav:"PK"`
	SK        string `dynamodbav:"SK"`
	SimRunID  string `dynamodbav:"simRunId"` // fresh uuid per run, used only for S3 pathing
	MeetingID string `dynamodbav:"meetingId"`
	UserID    string `dynamodbav:"userId"`
	Status    string `dynamodbav:"status"` // extracted | queued | running | done | error

	// Requirements is the JSON-encoded []SimRequirement -- the extracted
	// draft, then the user-confirmed set once a run starts. Small (a
	// handful of fields), so kept inline rather than in S3.
	Requirements string `dynamodbav:"requirements,omitempty"`
	// Options is the JSON-encoded []SimOption the user is comparing (2-3
	// architecture alternatives), confirmed alongside Requirements.
	Options string `dynamodbav:"options,omitempty"`

	ChartKeys        []string `dynamodbav:"chartKeys,omitempty"`
	ReportMarkdown   string   `dynamodbav:"reportMarkdown,omitempty"` // capped at simReportMaxBytes before write
	ReportKey        string   `dynamodbav:"reportKey,omitempty"`
	CodeKey          string   `dynamodbav:"codeKey,omitempty"`
	PriceSnapshotKey string   `dynamodbav:"priceSnapshotKey,omitempty"`
	PriceSnapshotAt  string   `dynamodbav:"priceSnapshotAt,omitempty"`
	Attempts         int      `dynamodbav:"attempts,omitempty"`
	ErrorMessage     string   `dynamodbav:"errorMessage,omitempty"`

	CreatedAt  time.Time `dynamodbav:"createdAt"`
	UpdatedAt  time.Time `dynamodbav:"updatedAt"`
	EntityType string    `dynamodbav:"entityType"` // "SIM_RUN"
}

// SimRequirement is one quantitative input to the simulation (users, TPS,
// data volume, SLO, ...). Key is drawn from a fixed server-side allowlist
// (see AllowedSimRequirementKeys) -- this is the boundary that keeps a
// meeting transcript from injecting arbitrary structure into the codegen
// prompt: only known keys with bounded values ever reach it.
type SimRequirement struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Value    string `json:"value"` // numeric-as-string, or an allowlisted enum value
	Unit     string `json:"unit,omitempty"`
	Required bool   `json:"required"`
	Source   string `json:"source"` // "extracted" | "user"
	// Evidence is a transcript://{segmentId} deep link (ADR-013) when the
	// value came from ExtractSimRequirements, empty when user-entered.
	Evidence string `json:"evidence,omitempty"`
}

// SimOption is one architecture alternative being compared (2-3 per run).
type SimOption struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SimRun status constants.
const (
	SimStatusExtracted = "extracted"
	SimStatusQueued    = "queued"
	SimStatusRunning   = "running"
	SimStatusDone      = "done"
	SimStatusError     = "error"
)

// PrefixSimRun is the fixed sort key for the SimRun singleton item.
const PrefixSimRun = "SIMRUN"

// SimRequirementSource values.
const (
	SimRequirementSourceExtracted = "extracted"
	SimRequirementSourceUser      = "user"
)

// ToSimRunResponse converts a stored SimRun into its API shape, re-parsing
// the JSON-string Requirements/Options fields into typed slices. Malformed
// stored JSON (should not happen -- only this codebase ever writes these
// fields) degrades to an empty slice rather than failing the whole response.
func ToSimRunResponse(run *SimRun) *SimRunResponse {
	if run == nil {
		return nil
	}
	var reqs []SimRequirement
	if run.Requirements != "" {
		_ = json.Unmarshal([]byte(run.Requirements), &reqs)
	}
	var opts []SimOption
	if run.Options != "" {
		_ = json.Unmarshal([]byte(run.Options), &opts)
	}
	charts := make([]SimChartResponse, 0, len(run.ChartKeys))
	for _, k := range run.ChartKeys {
		charts = append(charts, SimChartResponse{Key: k})
	}
	return &SimRunResponse{
		SimRunID:        run.SimRunID,
		Status:          run.Status,
		Requirements:    reqs,
		Options:         opts,
		Charts:          charts,
		ReportMarkdown:  run.ReportMarkdown,
		CodeKey:         run.CodeKey,
		PriceSnapshotAt: run.PriceSnapshotAt,
		ErrorMessage:    run.ErrorMessage,
		CreatedAt:       run.CreatedAt.Format(time.RFC3339),
		UpdatedAt:       run.UpdatedAt.Format(time.RFC3339),
	}
}
