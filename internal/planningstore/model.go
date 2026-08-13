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

type RunView struct {
	Run
	PlanVersion  int
	Plan         json.RawMessage
	Evidence     json.RawMessage
	Verification json.RawMessage
	Result       json.RawMessage
}
