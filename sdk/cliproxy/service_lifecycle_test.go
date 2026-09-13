package cliproxy

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShutdownServiceOnRunExitCreatesFreshTimeout(t *testing.T) {
	wantErr := errors.New("shutdown failed")
	var capturedCtx context.Context

	err := shutdownServiceOnRunExit(func(ctx context.Context) error {
		capturedCtx = ctx
		select {
		case <-ctx.Done():
			t.Fatalf("shutdown context was already done: %v", ctx.Err())
		default:
		}

		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("shutdown context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining < 25*time.Second || remaining > 30*time.Second {
			t.Fatalf("shutdown deadline remaining = %s, want a fresh 30s timeout", remaining)
		}
		return wantErr
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("shutdown error = %v, want %v", err, wantErr)
	}
	if capturedCtx == nil {
		t.Fatal("shutdown callback was not invoked")
	}
	select {
	case <-capturedCtx.Done():
		if !errors.Is(capturedCtx.Err(), context.Canceled) {
			t.Fatalf("shutdown context error after callback = %v, want context.Canceled", capturedCtx.Err())
		}
	default:
		t.Fatal("shutdown context was not cancelled after callback returned")
	}
}
