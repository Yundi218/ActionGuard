package llm

import (
	"context"

	"github.com/Yundi218/ActionGuard/internal/agent"
	"github.com/Yundi218/ActionGuard/internal/policy"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/Yundi218/ActionGuard/internal/verifier"
)

type PlanRequest struct {
	UserMessage   string
	Evidence      []policy.Evidence
	ToolContracts []toolkit.Contract
}

type RepairRequest struct {
	Original PlanRequest
	Plan     agent.ActionPlan
	Errors   []verifier.Error
}

type Planner interface {
	Plan(context.Context, PlanRequest) (agent.ActionPlan, error)
	Repair(context.Context, RepairRequest) (agent.ActionPlan, error)
}

type BackoffFunc func(context.Context, int) error
