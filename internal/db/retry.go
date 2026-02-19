package db

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func retryOnBusy(ctx context.Context, maxAttempts int, baseDelay time.Duration, fn func() error) error {
	if maxAttempts <= 0 {
		return fn()
	}
	if baseDelay <= 0 {
		baseDelay = 25 * time.Millisecond
	}

	delay := baseDelay
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := fn(); err != nil {

			fmt.Println("Attempting again due to an error", err)
			lastErr = err
			if !isBusyError(err) || attempt == maxAttempts {
				return err
			}
			jitter := time.Duration(rand.Int63n(int64(delay/2) + 1))
			select {
			case <-time.After(delay + jitter):
			case <-ctx.Done():
				return ctx.Err()
			}
			delay *= 2
			continue
		}
		return nil
	}
	return lastErr
}

func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	var sqlErr *sqlite.Error
	if !errors.As(err, &sqlErr) {
		return false
	}
	code := sqlErr.Code()
	if code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED {
		return true
	}
	base := code & 0xFF
	return base == sqlite3.SQLITE_BUSY || base == sqlite3.SQLITE_LOCKED
}
