package planningstore

import (
	"context"
	"encoding/json"
	"errors"
)

var (
	ErrNotFound      = errors.New("planning resource not found")
	ErrStateConflict = errors.New("planning state conflict")
	ErrConflict      = errors.New("planning resource conflict")
	ErrInvalid       = errors.New("invalid planning data")
	ErrStore         = errors.New("planning store error")
)

type Store interface {
	CreateSession(context.Context, Session) error
	GetSession(context.Context, string, string) (Session, error)
	CreateRun(context.Context, Run, string) error
	SavePlanningSnapshot(context.Context, string, string, string, PlanningSnapshot) error
	TransitionRun(context.Context, string, string, string, json.RawMessage, string, string) error
	GetRunView(context.Context, string, string) (RunView, error)
}
