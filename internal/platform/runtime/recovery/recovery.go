// Package recovery performs an explicitly requested, bounded observation pass.
// It has no access to runtime mutation methods and is not wired to the worker.
package recovery

import (
	"context"
	"errors"
	"time"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/platform/runtime/intent"
	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
)

var (
	ErrInvalid = errors.New("recovery: invalid request")
	ErrStore   = errors.New("recovery: store unavailable")
)

// Store is intentionally limited to reading intentions and recording observations.
type Store interface {
	ListRecoverable(context.Context, *rt.Identity, int) ([]intent.Intent, error)
	RecordObservation(context.Context, rt.Identity, uint64, intent.Phase, reconcile.Observation) (intent.Intent, error)
}

// Result records progress, not permission to retry a runtime operation. Next is
// the last processed key; nil means no entry was processed. Complete describes
// only this traversal, not a transactional snapshot of concurrently added rows.
type Result struct {
	Next      *rt.Identity
	Processed int
	Conflicts int
	Complete  bool
}

// Pass inspects at most maxItems entries in lexicographic order. A caller can
// resume from Next in a later explicitly invoked pass. New entries before the
// cursor require a new traversal from nil. It never invokes a runtime mutation.
// timeout bounds each inspection when the inspector honors its context; parent
// cancellation stops without persisting the current observation. A result
// returned after the inspection deadline is recorded as unknown. Store conflicts
// are counted and never retried.
func Pass(ctx context.Context, store Store, inspector reconcile.Inspector, after *rt.Identity, maxItems int, timeout time.Duration) (Result, error) {
	var out Result
	if store == nil || inspector == nil || maxItems < 1 || maxItems > 1000 || timeout <= 0 || timeout > time.Minute || (after != nil && !after.Valid()) {
		return out, ErrInvalid
	}
	cursor := after
	for out.Processed < maxItems {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		limit := maxItems - out.Processed
		if limit > 100 {
			limit = 100
		}
		page, err := store.ListRecoverable(ctx, cursor, limit)
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, ErrStore
		}
		for _, item := range page {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			checkCtx, cancel := context.WithTimeout(ctx, timeout)
			observation := reconcile.Observe(checkCtx, inspector, item.Ref)
			if checkCtx.Err() != nil {
				observation = reconcile.Unknown
			}
			cancel()
			if err := ctx.Err(); err != nil {
				return out, err
			}
			_, err := store.RecordObservation(ctx, item.Ref.Identity, item.Version, item.Phase, observation)
			if err != nil {
				if ctx.Err() != nil {
					return out, ctx.Err()
				}
				if !errors.Is(err, intent.ErrConflict) {
					return out, ErrStore
				}
				out.Conflicts++
			}
			id := item.Ref.Identity
			out.Next = &id
			cursor = out.Next
			out.Processed++
		}
		if len(page) < limit {
			out.Complete = true
			return out, nil
		}
	}
	return out, nil
}
