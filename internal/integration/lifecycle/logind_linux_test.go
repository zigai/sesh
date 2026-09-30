//go:build linux

package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

var (
	errStopLogindTest  = errors.New("stop logind test")
	errRearmLogindTest = errors.New("rearm logind test")
)

func testLifecycleSignal(owner, name string, preparing bool) *dbus.Signal {
	return &dbus.Signal{
		Sender: owner,
		Path:   dbus.ObjectPath(LogindObjectPath),
		Name:   name,
		Body:   []any{preparing},
	}
}

func TestLifecycleInhibitorsUseSeparateDelayActions(t *testing.T) {
	t.Parallel()

	got := Inhibitors()
	want := [2]InhibitorSpec{
		{
			What: LogindShutdownInhibitorWhat,
			Who:  LogindInhibitorWho,
			Why:  LogindInhibitorWhy,
			Mode: LogindInhibitorMode,
		},
		{
			What: LogindSleepInhibitorWhat,
			Who:  LogindInhibitorWho,
			Why:  LogindInhibitorWhy,
			Mode: LogindInhibitorMode,
		},
	}

	if got != want {
		t.Fatalf("LifecycleInhibitors = %+v, want %+v", got, want)
	}
}

func TestLogindEventUsesMeasuredDelayAndRejectsMalformedSignals(t *testing.T) {
	t.Parallel()

	client := &Logind{delay: 4 * time.Second, owner: ":1.99"}
	now := time.Unix(100, 0)

	event, err := client.event(testLifecycleSignal(client.owner, LogindManagerIface+"."+LogindPrepareForShutdownSignal, true), now)
	if err != nil {
		t.Fatalf("event failed: %v", err)
	}

	if event.Kind != EventShutdown || event.Cycle != 1 || !event.Deadline.Equal(now.Add(4*time.Second)) {
		t.Fatalf("event = %+v", event)
	}

	if _, err := client.event(testLifecycleSignal(client.owner, "wrong", true), now); !errors.Is(err, ErrLogindSignal) {
		t.Fatalf("malformed event error = %v", err)
	}

	if _, err := client.event(testLifecycleSignal(client.owner, LogindManagerIface+"."+LogindPrepareForShutdownSignal, true), now); !errors.Is(err, ErrLogindDuplicate) {
		t.Fatalf("duplicate event error = %v", err)
	}

	client.owner = ":1.99"
	if err := client.ownerChange(&dbus.Signal{
		Sender: "org.freedesktop.DBus",
		Path:   dbus.ObjectPath("/org/freedesktop/DBus"),
		Name:   nameOwnerChangedSignal,
		Body:   []any{LogindBusName, ":1.99", ":1.100"},
	}); !errors.Is(err, ErrLogindOwnerChanged) {
		t.Fatalf("owner replacement error = %v", err)
	}
}

func TestLogindEventRejectsUnpairedAndConcurrentCycles(t *testing.T) {
	t.Parallel()

	client := &Logind{delay: time.Second, owner: ":1.99"}
	now := time.Unix(100, 0)
	shutdown := LogindManagerIface + "." + LogindPrepareForShutdownSignal
	sleep := LogindManagerIface + "." + LogindPrepareForSleepSignal

	if _, err := client.event(testLifecycleSignal(client.owner, shutdown, false), now); !errors.Is(err, ErrLogindSignal) {
		t.Fatalf("false without prepare error = %v, want ErrLogindSignal", err)
	}

	if _, err := client.event(testLifecycleSignal(client.owner, shutdown, true), now); err != nil {
		t.Fatalf("shutdown prepare failed: %v", err)
	}

	if _, err := client.event(testLifecycleSignal(client.owner, sleep, true), now); !errors.Is(err, ErrLogindDuplicate) {
		t.Fatalf("concurrent sleep prepare error = %v, want ErrLogindDuplicate", err)
	}

	if _, err := client.event(testLifecycleSignal(client.owner, sleep, false), now); !errors.Is(err, ErrLogindSignal) {
		t.Fatalf("mismatched cancellation error = %v, want ErrLogindSignal", err)
	}

	canceled, err := client.event(testLifecycleSignal(client.owner, shutdown, false), now)
	if err != nil || !canceled.Canceled || canceled.Cycle != 1 {
		t.Fatalf("shutdown cancellation = (%+v, %v), want cycle 1 canceled", canceled, err)
	}

	if _, err := client.event(testLifecycleSignal(client.owner, shutdown, false), now); !errors.Is(err, ErrLogindSignal) {
		t.Fatalf("duplicate cancellation error = %v, want ErrLogindSignal", err)
	}
}

type fakeLogind struct {
	events   chan Event
	released int
	rearmed  int
	rearmErr error
}

func (f *fakeLogind) Next(ctx context.Context) (Event, error) {
	select {
	case event := <-f.events:
		return event, nil
	case <-ctx.Done():
		return Event{}, errors.Join(errStopLogindTest, context.Cause(ctx))
	}
}
func (f *fakeLogind) Release() error              { f.released++; return nil }
func (f *fakeLogind) Rearm(context.Context) error { f.rearmed++; return f.rearmErr }

func TestRunLogindReleasesAfterHandlerAndRearmsCanceledCycle(t *testing.T) {
	t.Parallel()

	runtime := &fakeLogind{events: make(chan Event, 2)}
	runtime.events <- Event{Kind: EventShutdown, Cycle: 1, Deadline: time.Now().Add(time.Second)}

	runtime.events <- Event{Kind: EventShutdown, Cycle: 1, Canceled: true}

	stop := errStopLogindTest
	called := 0

	err := RunLogind(t.Context(), runtime, func(context.Context, Event) error {
		called++

		if runtime.released != 0 {
			t.Fatal("inhibitor released before handler returned")
		}

		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("RunLogind error = %v", err)
	}

	if called != 1 || runtime.released != 1 || runtime.rearmed != 0 {
		t.Fatalf("called=%d released=%d rearmed=%d", called, runtime.released, runtime.rearmed)
	}
}

func TestRunLogindDoesNotResumeAdmissionWhenRearmFails(t *testing.T) {
	t.Parallel()

	runtime := &fakeLogind{
		events:   make(chan Event, 1),
		rearmErr: errRearmLogindTest,
	}
	runtime.events <- Event{Kind: EventSleep, Cycle: 1, Canceled: true}

	handled := false
	err := RunLogind(t.Context(), runtime, func(context.Context, Event) error {
		handled = true

		return nil
	})

	if !errors.Is(err, errRearmLogindTest) {
		t.Fatalf("RunLogind error = %v, want rearm failure", err)
	}

	if handled {
		t.Fatal("canceled handler ran without a rearmed inhibitor")
	}
}

func TestRunLogindPassesMeasuredDeadlineAndReleasesAfterHandler(t *testing.T) {
	t.Parallel()

	runtime := &fakeLogind{events: make(chan Event, 1)}
	runtime.events <- Event{Kind: EventShutdown, Cycle: 1, Deadline: time.Now().Add(20 * time.Millisecond)}

	err := RunLogind(t.Context(), runtime, func(ctx context.Context, _ Event) error {
		<-ctx.Done()

		if runtime.released != 0 {
			t.Fatal("inhibitor released before deadline-aware handler returned")
		}

		return context.Cause(ctx)
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunLogind error = %v, want deadline exceeded", err)
	}

	if runtime.released != 1 {
		t.Fatalf("inhibitor releases = %d, want 1", runtime.released)
	}
}

func TestRunLogindRearmsCanceledCycle(t *testing.T) {
	t.Parallel()

	runtime := &fakeLogind{events: make(chan Event, 2)}
	runtime.events <- Event{Kind: EventSleep, Cycle: 1, Canceled: true}

	ctx, cancel := context.WithCancel(t.Context())

	runtime.events <- Event{Kind: EventSleep, Cycle: 2, Deadline: time.Now().Add(time.Second)}

	err := RunLogind(ctx, runtime, func(_ context.Context, event Event) error {
		if event.Canceled {
			if runtime.rearmed != 1 {
				t.Fatalf("canceled handler observed rearmed=%d, want inhibitor first", runtime.rearmed)
			}

			return nil
		}

		cancel()

		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunLogind error = %v", err)
	}

	if runtime.rearmed != 1 || runtime.released != 1 {
		t.Fatalf("rearmed=%d released=%d", runtime.rearmed, runtime.released)
	}
}
