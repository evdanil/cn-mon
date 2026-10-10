package history

import (
	"context"
	"fmt"
)

type canceledDBError struct{ cause, operation error }

func (e *canceledDBError) Error() string   { return fmt.Sprintf("%v: %v", e.cause, e.operation) }
func (e *canceledDBError) Unwrap() []error { return []error{e.cause, e.operation} }

type cleanupFailure struct{ err error }

func (e *cleanupFailure) Error() string { return e.err.Error() }
func (e *cleanupFailure) Unwrap() error { return e.err }

func markCleanup(err error) error {
	if err == nil {
		return nil
	}
	if _, alreadyMarked := err.(*cleanupFailure); alreadyMarked {
		return err
	}
	return &cleanupFailure{err: err}
}

// CancellationOnly reports whether err consists only of the requested
// cancellation cause, including a driver error tagged where that cancellation
// was observed. Cleanup and unrelated failures remain visible when joined.
func CancellationOnly(err error, cause error) bool {
	if err == nil || (cause != context.Canceled && cause != context.DeadlineExceeded) {
		return false
	}
	if containsCleanupFailure(err) {
		return false
	}
	return cancellationTreeOnly(err, cause)
}

func cancellationTreeOnly(err, cause error) bool {
	if err == nil {
		return false
	}
	if tagged, ok := err.(*canceledDBError); ok {
		return tagged.cause == cause && singleCauseChain(tagged.operation)
	}
	if err == cause {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		seen := false
		for _, branch := range joined.Unwrap() {
			if branch == nil {
				continue
			}
			seen = true
			if !cancellationTreeOnly(branch, cause) {
				return false
			}
		}
		return seen
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return cancellationTreeOnly(wrapped.Unwrap(), cause)
	}
	return false
}

func containsCleanupFailure(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := err.(*cleanupFailure); ok {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if containsCleanupFailure(child) {
				return true
			}
		}
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return containsCleanupFailure(wrapped.Unwrap())
	}
	return false
}

func singleCauseChain(err error) bool {
	for err != nil {
		if _, joined := err.(interface{ Unwrap() []error }); joined {
			return false
		}
		wrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return true
		}
		err = wrapped.Unwrap()
	}
	return false
}
