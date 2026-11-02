package intent

import (
	"context"
	"database/sql"
	"errors"
	"math"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
)

type Action struct {
	Kind        string                `json:"kind"`
	Phase       Phase                 `json:"phase"`
	Observation reconcile.Observation `json:"observation"`
	Version     uint64                `json:"-"`
}

func scanAction(row scanner) (Action, error) {
	var a Action
	if err := row.Scan(&a.Kind, &a.Phase, &a.Observation, &a.Version); err != nil {
		return Action{}, err
	}
	if (a.Kind != "stop" && a.Kind != "remove") || a.Version == 0 || a.Version > math.MaxInt64 || !((a.Phase == Dispatched && a.Observation == "") || (a.Phase == Observed && observationValid(a.Observation))) {
		return Action{}, ErrUnavailable
	}
	return a, nil
}

// LastAction returns the most recent action in the one-stop, one-remove lifecycle.
func (s *Store) LastAction(ctx context.Context, id rt.Identity) (Action, error) {
	if !id.Valid() {
		return Action{}, ErrInvalid
	}
	a, e := scanAction(s.db.QueryRowContext(ctx, `SELECT kind,phase,observation,version FROM runtime_actions WHERE installation=? AND service=? AND operation=? ORDER BY CASE kind WHEN 'remove' THEN 1 ELSE 0 END DESC LIMIT 1`, id.Installation, id.Service, id.Operation))
	if errors.Is(e, sql.ErrNoRows) {
		return Action{}, ErrNotFound
	}
	if e != nil {
		return Action{}, ErrUnavailable
	}
	return a, nil
}

// DispatchAction atomically persists a single permitted mutation before send.
// Existing actions never authorize a second send, even after an uncertain result.
func (s *Store) DispatchAction(ctx context.Context, id rt.Identity, kind string, expected uint64) (Intent, Action, bool, error) {
	if !id.Valid() || (kind != "stop" && kind != "remove") || expected == 0 || expected >= math.MaxInt64 {
		return Intent{}, Action{}, false, ErrInvalid
	}
	c, e := s.db.Conn(ctx)
	if e != nil {
		return Intent{}, Action{}, false, ErrUnavailable
	}
	defer c.Close()
	if _, e = c.ExecContext(ctx, `BEGIN IMMEDIATE`); e != nil {
		return Intent{}, Action{}, false, ErrUnavailable
	}
	defer c.ExecContext(context.Background(), `ROLLBACK`)
	v, e := scanIntent(c.QueryRowContext(ctx, `SELECT resource,fingerprint,image,revision,phase,observation,version FROM runtime_intents WHERE installation=? AND service=? AND operation=?`, id.Installation, id.Service, id.Operation), id)
	if errors.Is(e, sql.ErrNoRows) {
		return Intent{}, Action{}, false, ErrNotFound
	}
	if e != nil {
		return Intent{}, Action{}, false, ErrUnavailable
	}
	a, e := scanAction(c.QueryRowContext(ctx, `SELECT kind,phase,observation,version FROM runtime_actions WHERE installation=? AND service=? AND operation=? AND kind=?`, id.Installation, id.Service, id.Operation, kind))
	if e == nil {
		return v, a, false, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return Intent{}, Action{}, false, ErrUnavailable
	}
	if v.Phase != Observed || v.Version != expected {
		return Intent{}, Action{}, false, ErrConflict
	}
	if kind == "stop" && v.Observation != reconcile.Running {
		return Intent{}, Action{}, false, ErrConflict
	}
	if kind == "remove" && v.Observation != reconcile.Stopped && v.Observation != reconcile.Absent {
		return Intent{}, Action{}, false, ErrConflict
	}
	if kind == "remove" {
		prior, e := scanAction(c.QueryRowContext(ctx, `SELECT kind,phase,observation,version FROM runtime_actions WHERE installation=? AND service=? AND operation=? AND kind='stop'`, id.Installation, id.Service, id.Operation))
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return Intent{}, Action{}, false, ErrUnavailable
		}
		if e == nil && (prior.Phase != Observed || prior.Observation != reconcile.Stopped) {
			return Intent{}, Action{}, false, ErrConflict
		}
	}
	_, e = c.ExecContext(ctx, `INSERT INTO runtime_actions VALUES(?,?,?,?,'dispatched','',1)`, id.Installation, id.Service, id.Operation, kind)
	if e != nil {
		return Intent{}, Action{}, false, ErrUnavailable
	}
	if _, e = c.ExecContext(ctx, `COMMIT`); e != nil {
		return Intent{}, Action{}, false, ErrUnavailable
	}
	return v, Action{Kind: kind, Phase: Dispatched, Version: 1}, true, nil
}

// ObserveAction persists a read-only observation and updates the service version atomically.
func (s *Store) ObserveAction(ctx context.Context, id rt.Identity, kind string, actionVersion, serviceVersion uint64, obs reconcile.Observation) (Intent, Action, error) {
	if !id.Valid() || (kind != "stop" && kind != "remove") || !observationValid(obs) || actionVersion == 0 || actionVersion >= math.MaxInt64 || serviceVersion == 0 || serviceVersion >= math.MaxInt64 {
		return Intent{}, Action{}, ErrInvalid
	}
	c, e := s.db.Conn(ctx)
	if e != nil {
		return Intent{}, Action{}, ErrUnavailable
	}
	defer c.Close()
	if _, e = c.ExecContext(ctx, `BEGIN IMMEDIATE`); e != nil {
		return Intent{}, Action{}, ErrUnavailable
	}
	defer c.ExecContext(context.Background(), `ROLLBACK`)
	v, e := scanIntent(c.QueryRowContext(ctx, `SELECT resource,fingerprint,image,revision,phase,observation,version FROM runtime_intents WHERE installation=? AND service=? AND operation=?`, id.Installation, id.Service, id.Operation), id)
	if e != nil {
		return Intent{}, Action{}, ErrUnavailable
	}
	a, e := scanAction(c.QueryRowContext(ctx, `SELECT kind,phase,observation,version FROM runtime_actions WHERE installation=? AND service=? AND operation=? AND kind=?`, id.Installation, id.Service, id.Operation, kind))
	if e != nil {
		return Intent{}, Action{}, ErrUnavailable
	}
	if v.Version != serviceVersion || v.Phase != Observed || a.Version != actionVersion {
		return Intent{}, Action{}, ErrConflict
	}
	_, e = c.ExecContext(ctx, `UPDATE runtime_actions SET phase='observed',observation=?,version=version+1 WHERE installation=? AND service=? AND operation=? AND kind=?`, obs, id.Installation, id.Service, id.Operation, kind)
	if e != nil {
		return Intent{}, Action{}, ErrUnavailable
	}
	_, e = c.ExecContext(ctx, `UPDATE runtime_intents SET observation=?,version=version+1 WHERE installation=? AND service=? AND operation=?`, obs, id.Installation, id.Service, id.Operation)
	if e != nil {
		return Intent{}, Action{}, ErrUnavailable
	}
	if _, e = c.ExecContext(ctx, `COMMIT`); e != nil {
		return Intent{}, Action{}, ErrUnavailable
	}
	v.Observation = obs
	v.Version++
	a.Phase = Observed
	a.Observation = obs
	a.Version++
	return v, a, nil
}
