package planningstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) CreateSession(ctx context.Context, session Session) error {
	_, err := s.pool.Exec(ctx, `
		insert into sessions (id, user_id, region, created_at)
		values ($1, $2, $3, $4)
	`, session.ID, session.UserID, session.Region, session.CreatedAt)
	return mapDatabaseError(err)
}

func (s *PostgresStore) GetSession(ctx context.Context, sessionID, userID string) (Session, error) {
	var session Session
	err := s.pool.QueryRow(ctx, `
		select id, user_id, region, created_at
		from sessions
		where id = $1 and user_id = $2
	`, sessionID, userID).Scan(
		&session.ID,
		&session.UserID,
		&session.Region,
		&session.CreatedAt,
	)
	if err != nil {
		return Session{}, mapDatabaseError(err)
	}
	session.CreatedAt = session.CreatedAt.UTC()
	return session, nil
}

func (s *PostgresStore) CreateRun(ctx context.Context, run Run, userMessage string) error {
	return s.withTransaction(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			insert into runs (
				id, session_id, status, goal, failure_code, failure_detail, created_at, updated_at
			)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
		`, run.ID, run.SessionID, run.Status, run.Goal, run.FailureCode, run.FailureDetail, run.CreatedAt, run.UpdatedAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			insert into messages (session_id, run_id, role, content, created_at)
			values ($1, $2, 'user', $3, $4)
		`, run.SessionID, run.ID, userMessage, run.CreatedAt)
		return err
	})
}

func (s *PostgresStore) SavePlanningSnapshot(ctx context.Context, runID, from, to string, snapshot PlanningSnapshot) error {
	return s.withTransaction(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update runs
			set status = $3, goal = $4, updated_at = now()
			where id = $1 and status = $2
		`, runID, from, to, snapshot.Goal)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrStateConflict
		}
		_, err = tx.Exec(ctx, `
			insert into plans (run_id, plan_version, plan_json, evidence_json, verification_json)
			values ($1, $2, $3, $4, $5)
		`, runID, snapshot.PlanVersion, snapshot.Plan, snapshot.Evidence, snapshot.Verification)
		return err
	})
}

func (s *PostgresStore) TransitionRun(ctx context.Context, runID, from, to string, result RunResult, failureCode, failureDetail string) error {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return ErrInvalid
	}
	tag, err := s.pool.Exec(ctx, `
		update runs
		set status = $3,
		    result_json = $4,
		    failure_code = $5,
		    failure_detail = $6,
		    updated_at = now()
		where id = $1 and status = $2
	`, runID, from, to, resultJSON, failureCode, failureDetail)
	if err != nil {
		return mapDatabaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStateConflict
	}
	return nil
}

func (s *PostgresStore) GetRunView(ctx context.Context, runID, userID string) (RunView, error) {
	var view RunView
	var resultJSON json.RawMessage
	err := s.pool.QueryRow(ctx, `
		select run.id,
		       run.session_id,
		       run.status,
		       run.goal,
		       run.failure_code,
		       run.created_at,
		       run.updated_at,
		       coalesce(plan.plan_version, 0),
		       coalesce(plan.plan_json, 'null'::jsonb),
		       coalesce(plan.evidence_json, 'null'::jsonb),
		       coalesce(plan.verification_json, 'null'::jsonb),
		       run.result_json
		from runs run
		join sessions session on session.id = run.session_id
		left join lateral (
			select plan_version, plan_json, evidence_json, verification_json
			from plans
			where run_id = run.id
			order by plan_version desc
			limit 1
		) plan on true
		where run.id = $1 and session.user_id = $2
	`, runID, userID).Scan(
		&view.ID,
		&view.SessionID,
		&view.Status,
		&view.Goal,
		&view.FailureCode,
		&view.CreatedAt,
		&view.UpdatedAt,
		&view.PlanVersion,
		&view.Plan,
		&view.Evidence,
		&view.Verification,
		&resultJSON,
	)
	if err != nil {
		return RunView{}, mapDatabaseError(err)
	}
	if err := json.Unmarshal(resultJSON, &view.Result); err != nil {
		return RunView{}, ErrStore
	}
	view.CreatedAt = view.CreatedAt.UTC()
	view.UpdatedAt = view.UpdatedAt.UTC()
	return view, nil
}

func (s *PostgresStore) withTransaction(ctx context.Context, write func(pgx.Tx) error) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return mapDatabaseError(err)
	}
	defer func() {
		rollbackErr := tx.Rollback(ctx)
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) && err == nil {
			err = mapDatabaseError(rollbackErr)
		}
	}()

	if err := write(tx); err != nil {
		return mapDatabaseError(err)
	}
	return mapDatabaseError(tx.Commit(ctx))
}

func mapDatabaseError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrStateConflict) ||
		errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrStore) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}

	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return ErrStore
	}
	switch postgresError.Code {
	case "23503":
		return ErrNotFound
	case "23505":
		return ErrConflict
	case "22021", "22P02", "23502", "23514":
		return ErrInvalid
	default:
		return ErrStore
	}
}
