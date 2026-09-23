//go:build linux

package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/samber/oops"
	"golang.org/x/sys/unix"
)

const logindSignalBuffer = 8

const logindInhibitorCount = 2

const nameOwnerChangedFields = 3

const (
	logindPreparingShutdownProperty = "PreparingForShutdown"
	logindPreparingSleepProperty    = "PreparingForSleep"
	nameOwnerChangedSignal          = "org.freedesktop.DBus.NameOwnerChanged"
)

var (
	ErrLogindSignal       = errors.New("invalid logind lifecycle signal")
	ErrLogindNotInhibited = errors.New("logind delay inhibitor is not held")
	ErrLogindPreparing    = errors.New("logind is already preparing a lifecycle transition")
	ErrLogindOwnerChanged = errors.New("logind D-Bus owner changed")
	ErrLogindDuplicate    = errors.New("duplicate logind prepare signal")
	ErrLogindSignalQueue  = errors.New("logind lifecycle signal queue overflowed")
)

type logindEvent struct {
	event Event
	err   error
}

type Logind struct {
	conn       *dbus.Conn
	object     dbus.BusObject
	signals    chan *dbus.Signal
	events     chan logindEvent
	delay      time.Duration
	owner      string
	pumpDone   chan struct{}
	pumpCancel context.CancelFunc
	signalFeed *boundedSignalHandler

	mu         sync.Mutex
	inhibitors [logindInhibitorCount]*os.File
	cycle      uint64
	activeKind EventKind
}

type LogindRuntime interface {
	Next(ctx context.Context) (Event, error)
	Release() error
	Rearm(ctx context.Context) error
}

type LiveStatus struct {
	InhibitorsHeld bool
}

func OpenLogind(ctx context.Context) (*Logind, error) {
	signalFeed := newBoundedSignalHandler()

	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx), dbus.WithSignalHandler(signalFeed))
	if err != nil {
		return nil, oops.In("lifecycle").Code("logind_failed").With("operation", "open_logind").Wrap(err)
	}

	client, err := openLogindOnConnection(ctx, conn, signalFeed)
	if err != nil {
		return nil, oops.In("lifecycle").Code("logind_failed").With("operation", "open_logind").Wrap(errors.Join(err, conn.Close()))
	}

	return client, nil
}

func (l *Logind) Delay() time.Duration { return l.delay }

func (l *Logind) Status(ctx context.Context) (LiveStatus, error) {
	if err := l.verifyOwner(ctx); err != nil {
		return LiveStatus{}, oops.In("lifecycle").Code("logind_failed").With("operation", "status").Wrap(err)
	}

	if l.signalFeed.overflowed() {
		return LiveStatus{}, oops.In("lifecycle").Code("logind_failed").With("operation", "status").Wrap(ErrLogindSignalQueue)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	shutdownHeld := l.inhibitors[0] != nil

	sleepHeld := l.inhibitors[1] != nil
	if shutdownHeld != sleepHeld {
		return LiveStatus{}, oops.In("lifecycle").Code("logind_failed").With("operation", "status").Wrap(ErrLogindNotInhibited)
	}

	for _, inhibitor := range l.inhibitors {
		if inhibitor == nil {
			continue
		}

		if _, err := inhibitor.Stat(); err != nil {
			return LiveStatus{}, oops.In("lifecycle").Code("logind_failed").With("operation", "status").Wrap(errors.Join(ErrLogindNotInhibited, err))
		}
	}

	return LiveStatus{InhibitorsHeld: shutdownHeld}, nil
}

func (l *Logind) Next(ctx context.Context) (Event, error) {
	if l.signalFeed.overflowed() {
		return Event{}, oops.In("lifecycle").Code("logind_failed").With("operation", "next").Wrap(ErrLogindSignalQueue)
	}

	select {
	case <-ctx.Done():
		return Event{}, oops.In("lifecycle").Code("logind_failed").With("operation", "next").Wrap(context.Cause(ctx))
	case <-l.signalFeed.overflow:
		return Event{}, oops.In("lifecycle").Code("logind_failed").With("operation", "next").Wrap(ErrLogindSignalQueue)
	case received, ok := <-l.events:
		if !ok {
			return Event{}, oops.In("lifecycle").Code("logind_failed").With("operation", "next").Wrap(dbus.ErrClosed)
		}

		if received.err != nil {
			return received.event, oops.In("lifecycle").Code("logind_failed").With("operation", "next").Wrap(received.err)
		}

		return received.event, nil
	}
}

func (l *Logind) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.inhibitors[0] == nil && l.inhibitors[1] == nil {
		return oops.In("lifecycle").Code("logind_failed").With("operation", "release").Wrap(ErrLogindNotInhibited)
	}

	inhibitors := l.inhibitors
	l.inhibitors = [logindInhibitorCount]*os.File{}

	if err := closeInhibitors(inhibitors); err != nil {
		return oops.In("lifecycle").Code("logind_failed").With("operation", "release").Wrap(err)
	}

	return nil
}

func (l *Logind) Rearm(ctx context.Context) error {
	return l.rearm(ctx, l.acquireInhibitor)
}

func (l *Logind) Close() error {
	l.pumpCancel()
	l.conn.RemoveSignal(l.signals)
	<-l.pumpDone

	releaseErr := l.Release()
	if errors.Is(releaseErr, ErrLogindNotInhibited) {
		releaseErr = nil
	}

	return errors.Join(releaseErr, l.conn.Close())
}

func (l *Logind) rearm(
	ctx context.Context,
	acquire func(context.Context, InhibitorSpec) (*os.File, error),
) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	shutdownHeld := l.inhibitors[0] != nil

	sleepHeld := l.inhibitors[1] != nil
	if shutdownHeld != sleepHeld {
		inhibitors := l.inhibitors
		l.inhibitors = [logindInhibitorCount]*os.File{}

		return errors.Join(ErrLogindNotInhibited, closeInhibitors(inhibitors))
	}

	if shutdownHeld {
		return l.verifyReady(ctx)
	}

	if err := l.verifyOwner(ctx); err != nil {
		return err
	}

	pending := [logindInhibitorCount]*os.File{}

	for index, spec := range LifecycleInhibitors() {
		inhibitor, err := acquire(ctx, spec)
		if err != nil {
			return errors.Join(err, closeInhibitors(pending))
		}

		pending[index] = inhibitor
		if err := context.Cause(ctx); err != nil {
			return errors.Join(err, closeInhibitors(pending))
		}

		if err := l.verifyOwner(ctx); err != nil {
			return errors.Join(err, closeInhibitors(pending))
		}
	}

	if err := l.verifyReady(ctx); err != nil {
		return errors.Join(err, closeInhibitors(pending))
	}

	l.inhibitors = pending

	return nil
}

func (l *Logind) acquireInhibitor(ctx context.Context, spec InhibitorSpec) (*os.File, error) {
	var fd dbus.UnixFD
	if err := l.object.CallWithContext(
		ctx,
		LogindManagerIface+"."+LogindInhibitMethod,
		0,
		spec.What,
		spec.Who,
		spec.Why,
		spec.Mode,
	).Store(&fd); err != nil {
		return nil, fmt.Errorf("acquire %s logind delay inhibitor: %w", spec.What, err)
	}

	if fd < 0 {
		return nil, fmt.Errorf("acquire %s logind delay inhibitor: %w", spec.What, ErrLogindNotInhibited)
	}

	inhibitor := os.NewFile(uintptr(uint32(fd)), "sesh-logind-"+spec.What+"-inhibitor")
	if inhibitor == nil {
		return nil, errors.Join(
			fmt.Errorf("acquire %s logind delay inhibitor: %w", spec.What, ErrLogindNotInhibited),
			unix.Close(int(fd)),
		)
	}

	return inhibitor, nil
}

func (l *Logind) verifyOwner(ctx context.Context) error {
	var currentOwner string
	if err := l.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, LogindBusName).Store(&currentOwner); err != nil {
		return errors.Join(ErrLogindOwnerChanged, fmt.Errorf("revalidate logind D-Bus owner: %w", err))
	}

	if currentOwner != l.owner {
		return ErrLogindOwnerChanged
	}

	return nil
}

func (l *Logind) verifyReady(ctx context.Context) error {
	if err := l.verifyOwner(ctx); err != nil {
		return err
	}

	if err := verifyLogindIdle(ctx, l.object); err != nil {
		return err
	}

	if l.signalFeed.overflowed() {
		return ErrLogindSignalQueue
	}

	if err := context.Cause(ctx); err != nil {
		return fmt.Errorf("verify logind readiness: %w", err)
	}

	return nil
}

func RunLogind(ctx context.Context, runtime LogindRuntime, handle func(context.Context, Event) error) error {
	for {
		event, err := runtime.Next(ctx)
		if err != nil {
			return oops.In("lifecycle").Code("logind_failed").With("operation", "run_logind").Wrap(err)
		}

		if event.Canceled {
			if err := runtime.Rearm(ctx); err != nil {
				return oops.In("lifecycle").Code("logind_failed").With("operation", "run_logind").Wrap(err)
			}

			if err := handle(ctx, event); err != nil {
				return err
			}

			continue
		}

		eventCtx := ctx

		cancel := func() {}
		if !event.Deadline.IsZero() {
			eventCtx, cancel = context.WithDeadline(ctx, event.Deadline)
		}

		handleErr := handle(eventCtx, event)

		cancel()

		releaseErr := runtime.Release()
		if handleErr != nil || releaseErr != nil {
			return errors.Join(handleErr, releaseErr)
		}
	}
}

func closeInhibitors(inhibitors [logindInhibitorCount]*os.File) error {
	var shutdownErr error

	if inhibitors[0] != nil {
		if err := inhibitors[0].Close(); err != nil {
			shutdownErr = fmt.Errorf("release shutdown logind delay inhibitor: %w", err)
		}
	}

	var sleepErr error

	if inhibitors[1] != nil {
		if err := inhibitors[1].Close(); err != nil {
			sleepErr = fmt.Errorf("release sleep logind delay inhibitor: %w", err)
		}
	}

	return errors.Join(shutdownErr, sleepErr)
}

func openLogindOnConnection(ctx context.Context, conn *dbus.Conn, signalFeed *boundedSignalHandler) (*Logind, error) {
	object := conn.Object(LogindBusName, dbus.ObjectPath(LogindObjectPath))

	property, err := logindProperty(ctx, object, "InhibitDelayMaxUSec")
	if err != nil {
		return nil, fmt.Errorf("read logind inhibit delay: %w", err)
	}

	delayMicros, ok := property.Value().(uint64)

	const maxDurationMicros = uint64(math.MaxInt64) / uint64(time.Microsecond)
	if !ok || delayMicros == 0 || delayMicros > maxDurationMicros {
		return nil, fmt.Errorf("read logind inhibit delay: %w", ErrLogindSignal)
	}

	var owner string
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, LogindBusName).Store(&owner); err != nil {
		return nil, fmt.Errorf("resolve logind D-Bus owner: %w", err)
	}

	if err := subscribeLogindSignals(ctx, conn, owner); err != nil {
		return nil, err
	}

	pumpCtx, pumpCancel := context.WithCancel(context.WithoutCancel(ctx))

	client := &Logind{
		conn:       conn,
		object:     object,
		signals:    make(chan *dbus.Signal, logindSignalBuffer),
		events:     make(chan logindEvent, logindSignalBuffer),
		delay:      time.Duration(delayMicros) * time.Microsecond,
		owner:      owner,
		pumpDone:   make(chan struct{}),
		pumpCancel: pumpCancel,
		signalFeed: signalFeed,
		mu:         sync.Mutex{},
		inhibitors: [logindInhibitorCount]*os.File{},
		cycle:      0,
		activeKind: "",
	}
	conn.Signal(client.signals)

	if err := client.Rearm(ctx); err != nil {
		pumpCancel()
		conn.RemoveSignal(client.signals)

		return nil, err
	}

	if err := verifyLogindIdle(ctx, object); err != nil {
		pumpCancel()
		conn.RemoveSignal(client.signals)

		return nil, errors.Join(err, client.Release())
	}

	if err := client.verifyOwner(ctx); err != nil {
		pumpCancel()
		conn.RemoveSignal(client.signals)

		return nil, errors.Join(err, client.Release())
	}

	if signalFeed.overflowed() {
		pumpCancel()
		conn.RemoveSignal(client.signals)

		return nil, errors.Join(ErrLogindSignalQueue, client.Release())
	}

	go client.pump(pumpCtx)

	return client, nil
}

func subscribeLogindSignals(ctx context.Context, conn *dbus.Conn, owner string) error {
	for _, member := range []string{LogindPrepareForShutdownSignal, LogindPrepareForSleepSignal} {
		if err := conn.AddMatchSignalContext(
			ctx,
			dbus.WithMatchSender(owner),
			dbus.WithMatchObjectPath(dbus.ObjectPath(LogindObjectPath)),
			dbus.WithMatchInterface(LogindManagerIface),
			dbus.WithMatchMember(member),
		); err != nil {
			return fmt.Errorf("subscribe to logind lifecycle signal: %w", err)
		}
	}

	if err := conn.AddMatchSignalContext(
		ctx,
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, LogindBusName),
	); err != nil {
		return fmt.Errorf("subscribe to logind owner changes: %w", err)
	}

	return nil
}

func verifyLogindIdle(ctx context.Context, object dbus.BusObject) error {
	preparingShutdown, err := logindBoolProperty(ctx, object, logindPreparingShutdownProperty)
	if err != nil {
		return err
	}

	preparingSleep, err := logindBoolProperty(ctx, object, logindPreparingSleepProperty)
	if err != nil {
		return err
	}

	if preparingShutdown || preparingSleep {
		return ErrLogindPreparing
	}

	return nil
}

func logindProperty(ctx context.Context, object dbus.BusObject, name string) (dbus.Variant, error) {
	var property dbus.Variant
	if err := object.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, LogindManagerIface, name).Store(&property); err != nil {
		return dbus.Variant{}, fmt.Errorf("read logind property: %w", err)
	}

	return property, nil
}

func logindBoolProperty(ctx context.Context, object dbus.BusObject, name string) (bool, error) {
	property, err := logindProperty(ctx, object, name)
	if err != nil {
		return false, err
	}

	value, ok := property.Value().(bool)
	if !ok {
		return false, ErrLogindSignal
	}

	return value, nil
}

func (l *Logind) pump(ctx context.Context) {
	defer close(l.pumpDone)
	defer close(l.events)

	for {
		select {
		case <-ctx.Done():
			return
		case <-l.signalFeed.overflow:
			return
		case signal, ok := <-l.signals:
			if !ok {
				return
			}

			received := l.decodeSignal(signal)

			select {
			case l.events <- received:
			case <-l.signalFeed.overflow:
				return
			case <-ctx.Done():
				return
			}
		}
	}
}

func (l *Logind) decodeSignal(signal *dbus.Signal) logindEvent {
	received := logindEvent{event: Event{Kind: "", Cycle: 0, Deadline: time.Time{}, Canceled: false}, err: nil}
	receivedAt, known := l.signalFeed.receivedAt(signal)

	switch {
	case !known:
		received.err = ErrLogindSignal
	case signal != nil && signal.Name == nameOwnerChangedSignal:
		received.err = l.ownerChange(signal)
	default:
		received.event, received.err = l.event(signal, receivedAt)
	}

	return received
}

func (l *Logind) ownerChange(signal *dbus.Signal) error {
	if signal == nil || signal.Sender != "org.freedesktop.DBus" ||
		signal.Path != dbus.ObjectPath("/org/freedesktop/DBus") || len(signal.Body) != nameOwnerChangedFields {
		return ErrLogindSignal
	}

	name, nameOK := signal.Body[0].(string)
	oldOwner, oldOK := signal.Body[1].(string)

	newOwner, newOK := signal.Body[2].(string)
	if !nameOK || !oldOK || !newOK || name != LogindBusName {
		return ErrLogindSignal
	}

	if oldOwner == newOwner {
		return ErrLogindSignal
	}

	return ErrLogindOwnerChanged
}

func (l *Logind) event(signal *dbus.Signal, now time.Time) (Event, error) {
	if signal == nil || signal.Sender != l.owner || signal.Path != dbus.ObjectPath(LogindObjectPath) || len(signal.Body) != 1 {
		return Event{}, ErrLogindSignal
	}

	preparing, ok := signal.Body[0].(bool)
	if !ok {
		return Event{}, ErrLogindSignal
	}

	var kind EventKind

	switch signal.Name {
	case LogindManagerIface + "." + LogindPrepareForShutdownSignal:
		kind = EventShutdown
	case LogindManagerIface + "." + LogindPrepareForSleepSignal:
		kind = EventSleep
	default:
		return Event{}, ErrLogindSignal
	}

	if preparing {
		if l.activeKind != "" {
			return Event{}, ErrLogindDuplicate
		}

		l.activeKind = kind
		l.cycle++

		return Event{Kind: kind, Cycle: l.cycle, Deadline: now.Add(l.delay), Canceled: false}, nil
	}

	if l.activeKind != kind {
		return Event{}, ErrLogindSignal
	}

	l.activeKind = ""

	return Event{Kind: kind, Cycle: l.cycle, Deadline: time.Time{}, Canceled: true}, nil
}
