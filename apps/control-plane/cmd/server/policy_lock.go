package main

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// policyAdvisoryLockKey identifies the PostgreSQL advisory lock that
// serialises policy publish, rollback and OPA sync across every control-plane
// replica that shares the database.
const policyAdvisoryLockKey int64 = 0x5355455A504F4C // "SUEZPOL"

// lockPolicies takes the in-process mutex and then the database advisory
// lock. The advisory lock lives on its own connection and is released by the
// returned function; if this process dies, PostgreSQL releases it when the
// connection closes, so a crashed replica cannot block the others.
func (a *controlPlaneApp) lockPolicies(ctx context.Context) (func(), error) {
	a.policyMu.Lock()
	if a.db == nil {
		return a.policyMu.Unlock, nil
	}
	lockCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := a.db.Conn(lockCtx)
	if err != nil {
		a.policyMu.Unlock()
		return nil, err
	}
	if _, err := conn.ExecContext(lockCtx, `select pg_advisory_lock($1)`, policyAdvisoryLockKey); err != nil {
		_ = conn.Close()
		a.policyMu.Unlock()
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.New("another policy change is still in progress; try again")
		}
		return nil, err
	}
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(unlockCtx, `select pg_advisory_unlock($1)`, policyAdvisoryLockKey); err != nil {
			slog.Error("policy lock release failed; closing the connection releases it", "error", err.Error())
		}
		_ = conn.Close()
		a.policyMu.Unlock()
	}, nil
}

// runPolicyReconciler re-applies the published policy set to OPA on a timer.
// The sync is idempotent (put desired modules, then delete stale ones), so
// this repairs an OPA that restarted empty, a replica that crashed mid-sync,
// and a change published by another replica that uses a different OPA.
func (a *controlPlaneApp) runPolicyReconciler(ctx context.Context) {
	interval := 30 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			syncCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			if err := a.syncPublishedPolicies(syncCtx, ""); err != nil {
				slog.Warn("policy reconcile failed", "error", err.Error())
			}
			cancel()
		}
	}
}
