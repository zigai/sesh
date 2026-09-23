//go:build darwin

package process

import (
	"context"
	"errors"
	"math"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinStartTimeValidatesAndCombinesTimeval(t *testing.T) {
	got, err := darwinStartTime(123, 456789)
	if err != nil {
		t.Fatalf("darwinStartTime: %v", err)
	}

	if got != 123456789 {
		t.Fatalf("darwinStartTime = %d, want 123456789", got)
	}

	for _, input := range []struct {
		seconds      int64
		microseconds int64
	}{
		{seconds: -1},
		{microseconds: -1},
		{microseconds: microsecondsPerSecond},
		{seconds: int64(math.MaxInt64), microseconds: 1},
	} {
		_, err := darwinStartTime(input.seconds, input.microseconds)
		if !errors.Is(err, ErrMalformedIdentity) {
			t.Fatalf("darwinStartTime(%d, %d) error = %v, want %v", input.seconds, input.microseconds, err, ErrMalformedIdentity)
		}
	}
}

func TestObserveDarwinCancellation(t *testing.T) {
	req := Request{
		PID:           1,
		ExpectedBirth: Identity{BootID: "11111111-2222-3333-4444-555555555555", StartTime: 1},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Observe(ctx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Observe error = %v, want context cancellation", err)
	}

	_, err = Observe(context.Background(), req)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Observe error = %v, want %v", err, ErrUnsupported)
	}
}

func TestClassifyDarwinProcessError(t *testing.T) {
	for _, vanished := range []error{unix.ESRCH, unix.ENOENT} {
		if got := classifyDarwinProcessError(vanished); !errors.Is(got, ErrProcessVanished) {
			t.Fatalf("classifyDarwinProcessError(%v) = %v, want %v", vanished, got, ErrProcessVanished)
		}
	}

	if got := classifyDarwinProcessError(unix.EPERM); !errors.Is(got, unix.EPERM) {
		t.Fatalf("classifyDarwinProcessError(EPERM) = %v, want preserved EPERM", got)
	}
}
