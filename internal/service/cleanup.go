package service

import (
	"context"
	"log"
	"time"
)

type ExpiredDataRepository interface {
	DeleteExpired(ctx context.Context, retention time.Duration) (sessions, verifications int64, err error)
}

// RunCleanup deletes expired auth data immediately and then every interval
// until ctx is cancelled. Errors are logged and retried on the next tick.
func RunCleanup(ctx context.Context, repository ExpiredDataRepository, interval, retention time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		runCtx, cancel := context.WithTimeout(ctx, time.Minute)
		sessions, verifications, err := repository.DeleteExpired(runCtx, retention)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			log.Printf("cleanup expired auth data: %v", err)
		case sessions > 0 || verifications > 0:
			log.Printf("cleanup: deleted %d refresh sessions and %d email verifications", sessions, verifications)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
