package planningstore

import (
	"encoding/json"
	"time"
)

type Session struct {
	ID        string
	UserID    string
	Region    string
	CreatedAt time.Time
}

type Run struct {
	ID            string
	SessionID     string
	Status        string
	Goal          string
	FailureCode   string
	FailureDetail string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type PlanningSnapshot struct {
	Goal         string
	PlanVersion  int
	Plan         json.RawMessage
	Evidence     json.RawMessage
	Verification json.RawMessage
}

type RunStepResult struct {
	StepID   string `json:"step_id"`
	Tool     string `json:"tool"`
	Status   string `json:"status"`
	Replayed bool   `json:"replayed,omitempty"`
}

type RunResult struct {
	Summary string          `json:"summary,omitempty"`
	Steps   []RunStepResult `json:"steps,omitempty"`
}

type RunView struct {
	ID           string
	SessionID    string
	Status       string
	Goal         string
	FailureCode  string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	PlanVersion  int
	Plan         json.RawMessage
	Evidence     json.RawMessage
	Verification json.RawMessage
	Result       RunResult
}
