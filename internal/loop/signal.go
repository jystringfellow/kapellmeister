package loop

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

var lastSignal atomic.Pointer[SignalError]

func LastSignal() *SignalError { return lastSignal.Load() }

type SignalError struct{ Signal os.Signal }

func (e *SignalError) Error() string {
	name := e.Signal.String()
	switch e.Signal {
	case syscall.SIGINT:
		name = "SIGINT"
	case syscall.SIGTERM:
		name = "SIGTERM"
	case syscall.SIGHUP:
		name = "SIGHUP"
	}
	return "interrupted by " + name
}

func (e *SignalError) Is(target error) bool { return target == context.Canceled }

func SignalContext(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, sigs...)
	go func() {
		select {
		case sig := <-signals:
			signal.Stop(signals)
			cause := &SignalError{Signal: sig}
			lastSignal.Store(cause)
			cancel(cause)
		case <-ctx.Done():
		}
	}()
	return ctx, func() { signal.Stop(signals); cancel(context.Canceled) }
}

func SignalOf(ctx context.Context) *SignalError {
	var cause *SignalError
	if errors.As(context.Cause(ctx), &cause) {
		return cause
	}
	return nil
}

func cancellationCause(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) {
		if cause := SignalOf(ctx); cause != nil {
			return cause
		}
	}
	return err
}

func cancellationReason(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	return cancellationCause(ctx, err).Error()
}
