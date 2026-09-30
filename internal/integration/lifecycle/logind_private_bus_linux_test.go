//go:build linux

package lifecycle

import (
	"bufio"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

type fakeLogindManager struct {
	mu        sync.Mutex
	readers   []*os.File
	writers   []*os.File
	requests  []InhibitorSpec
	failCall  int
	afterCall func(int)
}

func (f *fakeLogindManager) Inhibit(what, who, why, mode string) (dbus.UnixFD, *dbus.Error) {
	if (what != LogindShutdownInhibitorWhat && what != LogindSleepInhibitorWhat) ||
		who != LogindInhibitorWho || why != LogindInhibitorWhy || mode != LogindInhibitorMode {
		return -1, dbus.MakeFailedError(ErrLogindSignal)
	}

	f.mu.Lock()
	f.requests = append(f.requests, InhibitorSpec{What: what, Who: who, Why: why, Mode: mode})
	call := len(f.requests)
	fail := f.failCall == call
	afterCall := f.afterCall
	f.mu.Unlock()

	if fail {
		return -1, dbus.MakeFailedError(ErrLogindNotInhibited)
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		return -1, dbus.MakeFailedError(err)
	}

	writerFD := writer.Fd()
	if writerFD > math.MaxInt32 {
		_ = errors.Join(reader.Close(), writer.Close())

		return -1, dbus.MakeFailedError(ErrLogindSignal)
	}

	f.mu.Lock()
	f.readers = append(f.readers, reader)
	f.writers = append(f.writers, writer)
	f.mu.Unlock()

	if afterCall != nil {
		afterCall(call)
	}

	return dbus.UnixFD(int32(writerFD)), nil
}

func (f *fakeLogindManager) configure(failCall int, afterCall func(int)) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.failCall = failCall
	f.afterCall = afterCall
}

func (f *fakeLogindManager) requestsSnapshot() []InhibitorSpec {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]InhibitorSpec(nil), f.requests...)
}

func (f *fakeLogindManager) close() {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, file := range append(f.readers, f.writers...) {
		if file == nil {
			continue
		}

		_ = file.Close()
	}
}

func (f *fakeLogindManager) closeWriters() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	var result error

	for index, writer := range f.writers {
		if writer == nil {
			continue
		}

		result = errors.Join(result, writer.Close())
		f.writers[index] = nil
	}

	return result
}

func (f *fakeLogindManager) reader(index int) *os.File {
	f.mu.Lock()
	defer f.mu.Unlock()

	if index < 0 || index >= len(f.readers) {
		return nil
	}

	return f.readers[index]
}

func (f *fakeLogindManager) readerCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.readers)
}

type fakeLogindProperties struct {
	mu                sync.Mutex
	preparingShutdown bool
	preparingSleep    bool
}

func (f *fakeLogindProperties) Get(iface, property string) (dbus.Variant, *dbus.Error) {
	if iface != LogindManagerIface {
		return dbus.Variant{}, dbus.MakeFailedError(ErrLogindSignal)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	switch property {
	case "InhibitDelayMaxUSec":
		return dbus.MakeVariant(uint64((3 * time.Second) / time.Microsecond)), nil
	case logindPreparingShutdownProperty:
		return dbus.MakeVariant(f.preparingShutdown), nil
	case logindPreparingSleepProperty:
		return dbus.MakeVariant(f.preparingSleep), nil
	default:
		return dbus.Variant{}, dbus.MakeFailedError(ErrLogindSignal)
	}
}

func (f *fakeLogindProperties) setPreparing(shutdown, sleep bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.preparingShutdown = shutdown
	f.preparingSleep = sleep
}

func TestOpenLogindUsesPrivateBusWireContract(t *testing.T) {
	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)
	manager := exportFakeLogind(t, service, &fakeLogindProperties{})
	t.Cleanup(manager.close)
	t.Cleanup(func() { _ = service.Close() })

	signalFeed := newBoundedSignalHandler()
	clientConn := connectPrivateBusWithSignals(t, address, signalFeed)

	client, err := openLogindOnConnection(t.Context(), clientConn, signalFeed)
	if err != nil {
		_ = clientConn.Close()

		t.Fatalf("openLogindOnConnection failed: %v", err)
	}

	if client.Delay() != 3*time.Second {
		t.Fatalf("delay = %s, want 3s", client.Delay())
	}

	assertLiveStatus(t, client, true)

	expectedRequests := Inhibitors()
	assertInhibitorRequests(t, manager.requestsSnapshot(), expectedRequests[:])

	if err := manager.closeWriters(); err != nil {
		t.Fatalf("close service inhibitor descriptors: %v", err)
	}

	assertPrivateBusShutdownSignal(t, service, client)
	assertPrivateBusInhibitorRelease(t, manager, client, 0, 1)

	assertLiveStatus(t, client, false)

	assertPrivateBusCanceledSignal(t, service, client)

	if err := client.Rearm(t.Context()); err != nil {
		t.Fatalf("Rearm failed: %v", err)
	}

	assertLiveStatus(t, client, true)

	if err := manager.closeWriters(); err != nil {
		t.Fatalf("close rearmed service inhibitor descriptors: %v", err)
	}

	assertPrivateBusInhibitorRelease(t, manager, client, 2, 3)

	if err := client.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func assertLiveStatus(t *testing.T, client *Logind, held bool) {
	t.Helper()

	status, err := client.Status(t.Context())
	if err != nil || status.InhibitorsHeld != held {
		t.Fatalf("live status = (%+v, %v), want held=%t", status, err, held)
	}
}

func TestOpenLogindClosesFirstInhibitorWhenSecondAcquisitionFails(t *testing.T) {
	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)
	manager := exportFakeLogind(t, service, &fakeLogindProperties{})
	manager.configure(2, nil)
	t.Cleanup(manager.close)
	t.Cleanup(func() { _ = service.Close() })

	signalFeed := newBoundedSignalHandler()
	clientConn := connectPrivateBusWithSignals(t, address, signalFeed)

	client, err := openLogindOnConnection(t.Context(), clientConn, signalFeed)
	if client != nil || err == nil {
		_ = clientConn.Close()

		t.Fatalf("openLogindOnConnection = (%v, %v), want second acquisition failure", client, err)
	}

	expectedRequests := Inhibitors()
	assertInhibitorRequests(t, manager.requestsSnapshot(), expectedRequests[:])
	closeManagerWriters(t, manager)
	assertReaderClosed(t, manager.reader(0))

	if err := clientConn.Close(); err != nil {
		t.Fatalf("close client connection: %v", err)
	}
}

func TestLogindRearmClosesFirstInhibitorWhenCanceledBetweenAcquisitions(t *testing.T) {
	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)
	manager := exportFakeLogind(t, service, &fakeLogindProperties{})
	t.Cleanup(manager.close)
	t.Cleanup(func() { _ = service.Close() })

	signalFeed := newBoundedSignalHandler()
	clientConn := connectPrivateBusWithSignals(t, address, signalFeed)

	client, err := openLogindOnConnection(t.Context(), clientConn, signalFeed)
	if err != nil {
		_ = clientConn.Close()

		t.Fatalf("openLogindOnConnection failed: %v", err)
	}

	t.Cleanup(func() { _ = client.Close() })

	if err := client.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())

	err = client.rearm(ctx, func(ctx context.Context, spec InhibitorSpec) (*os.File, error) {
		inhibitor, acquireErr := client.acquireInhibitor(ctx, spec)

		cancel()

		return inhibitor, acquireErr
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Rearm canceled between acquisitions error = %v, want context cancellation", err)
	}

	expectedRequests := Inhibitors()
	assertInhibitorRequests(
		t,
		manager.requestsSnapshot(),
		append(expectedRequests[:], ShutdownInhibitor()),
	)
	closeManagerWriters(t, manager)
	assertReaderClosed(t, manager.reader(2))
}

func TestOpenLogindClosesPairWhenTransitionStartsDuringAcquisition(t *testing.T) {
	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)
	properties := &fakeLogindProperties{}
	manager := exportFakeLogind(t, service, properties)
	manager.configure(0, func(call int) {
		if call == 1 {
			properties.setPreparing(true, false)
		}
	})
	t.Cleanup(manager.close)
	t.Cleanup(func() { _ = service.Close() })

	signalFeed := newBoundedSignalHandler()
	clientConn := connectPrivateBusWithSignals(t, address, signalFeed)

	client, err := openLogindOnConnection(t.Context(), clientConn, signalFeed)
	if client != nil || !errors.Is(err, ErrLogindPreparing) {
		_ = clientConn.Close()

		t.Fatalf("openLogindOnConnection = (%v, %v), want ErrLogindPreparing", client, err)
	}

	closeManagerWriters(t, manager)
	assertReaderClosed(t, manager.reader(0))
	assertReaderClosed(t, manager.reader(1))

	if err := clientConn.Close(); err != nil {
		t.Fatalf("close client connection: %v", err)
	}
}

func assertPrivateBusShutdownSignal(t *testing.T, service *dbus.Conn, client *Logind) {
	t.Helper()

	receivedAt := time.Now()

	if err := service.Emit(
		dbus.ObjectPath(LogindObjectPath),
		LogindManagerIface+"."+LogindPrepareForShutdownSignal,
		true,
	); err != nil {
		t.Fatalf("emit shutdown signal: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	eventCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	dequeuedAt := time.Now()
	event, err := client.Next(eventCtx)

	cancel()

	if err != nil {
		t.Fatalf("Next failed: %v", err)
	}

	if event.Kind != EventShutdown || event.Canceled || event.Deadline.Before(receivedAt.Add(2500*time.Millisecond)) ||
		event.Deadline.After(dequeuedAt.Add(2900*time.Millisecond)) {
		t.Fatalf("event = %+v, want measured shutdown deadline", event)
	}
}

func assertPrivateBusCanceledSignal(t *testing.T, service *dbus.Conn, client *Logind) {
	t.Helper()

	if err := service.Emit(
		dbus.ObjectPath(LogindObjectPath),
		LogindManagerIface+"."+LogindPrepareForShutdownSignal,
		false,
	); err != nil {
		t.Fatalf("emit canceled shutdown signal: %v", err)
	}

	eventCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	event, err := client.Next(eventCtx)

	cancel()

	if err != nil {
		t.Fatalf("receive canceled shutdown signal: %v", err)
	}

	if event.Kind != EventShutdown || !event.Canceled || event.Cycle != 1 {
		t.Fatalf("canceled event = %+v, want shutdown cycle 1 cancellation", event)
	}
}

func assertPrivateBusInhibitorRelease(t *testing.T, manager *fakeLogindManager, client *Logind, readerIndexes ...int) {
	t.Helper()

	released := make(chan error, len(readerIndexes))
	for _, readerIndex := range readerIndexes {
		reader := manager.reader(readerIndex)
		if reader == nil {
			t.Fatalf("fake logind did not retain inhibitor read descriptor %d", readerIndex)
		}

		go func() {
			buffer := make([]byte, 1)
			_, readErr := reader.Read(buffer)

			released <- readErr
		}()
	}

	if err := client.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	for range readerIndexes {
		select {
		case readErr := <-released:
			if !errors.Is(readErr, io.EOF) {
				t.Fatalf("inhibitor read after release = %v, want EOF", readErr)
			}
		case <-time.After(time.Second):
			t.Fatal("inhibitor descriptor remained open after Release")
		}
	}
}

func TestOpenLogindFailsClosedWhenSignalQueueOverflows(t *testing.T) {
	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)
	manager := exportFakeLogind(t, service, &fakeLogindProperties{})
	t.Cleanup(manager.close)
	t.Cleanup(func() { _ = service.Close() })

	signalFeed := newBoundedSignalHandler()
	clientConn := connectPrivateBusWithSignals(t, address, signalFeed)

	client, err := openLogindOnConnection(t.Context(), clientConn, signalFeed)
	if err != nil {
		_ = clientConn.Close()

		t.Fatalf("openLogindOnConnection failed: %v", err)
	}

	t.Cleanup(func() { _ = client.Close() })

	for index := range 4 * logindSignalBuffer {
		if err := service.Emit(
			dbus.ObjectPath(LogindObjectPath),
			LogindManagerIface+"."+LogindPrepareForShutdownSignal,
			index%2 == 0,
		); err != nil {
			t.Fatalf("emit lifecycle signal %d: %v", index, err)
		}
	}

	deadline := time.Now().Add(time.Second)
	for !signalFeed.overflowed() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	if _, err := client.Next(t.Context()); !errors.Is(err, ErrLogindSignalQueue) {
		t.Fatalf("Next after queue overflow error = %v, want ErrLogindSignalQueue", err)
	}
}

func TestOpenLogindIgnoresPrepareSignalFromRoguePrivateBusSender(t *testing.T) {
	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)
	manager := exportFakeLogind(t, service, &fakeLogindProperties{})
	t.Cleanup(manager.close)
	t.Cleanup(func() { _ = service.Close() })

	rogue := connectPrivateBus(t, address)
	t.Cleanup(func() { _ = rogue.Close() })

	signalFeed := newBoundedSignalHandler()
	clientConn := connectPrivateBusWithSignals(t, address, signalFeed)

	client, err := openLogindOnConnection(t.Context(), clientConn, signalFeed)
	if err != nil {
		_ = clientConn.Close()

		t.Fatalf("openLogindOnConnection failed: %v", err)
	}

	t.Cleanup(func() { _ = client.Close() })

	if err := rogue.Emit(
		dbus.ObjectPath(LogindObjectPath),
		LogindManagerIface+"."+LogindPrepareForShutdownSignal,
		true,
	); err != nil {
		t.Fatalf("emit rogue shutdown signal: %v", err)
	}

	rogueCtx, rogueCancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	_, rogueErr := client.Next(rogueCtx)

	rogueCancel()

	if !errors.Is(rogueErr, context.DeadlineExceeded) {
		t.Fatalf("Next after rogue signal error = %v, want deadline exceeded", rogueErr)
	}

	if err := service.Emit(
		dbus.ObjectPath(LogindObjectPath),
		LogindManagerIface+"."+LogindPrepareForShutdownSignal,
		true,
	); err != nil {
		t.Fatalf("emit owned shutdown signal: %v", err)
	}

	nextCtx, nextCancel := context.WithTimeout(t.Context(), time.Second)
	event, nextErr := client.Next(nextCtx)

	nextCancel()

	if nextErr != nil || event.Kind != EventShutdown || event.Canceled {
		t.Fatalf("owned shutdown event = (%+v, %v)", event, nextErr)
	}
}

func TestOpenLogindReportsPrivateBusConnectionLoss(t *testing.T) {
	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)
	manager := exportFakeLogind(t, service, &fakeLogindProperties{})
	t.Cleanup(manager.close)
	t.Cleanup(func() { _ = service.Close() })

	signalFeed := newBoundedSignalHandler()
	clientConn := connectPrivateBusWithSignals(t, address, signalFeed)

	client, err := openLogindOnConnection(t.Context(), clientConn, signalFeed)
	if err != nil {
		_ = clientConn.Close()

		t.Fatalf("openLogindOnConnection failed: %v", err)
	}

	if err := clientConn.Close(); err != nil {
		t.Fatalf("close client connection: %v", err)
	}

	nextCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	_, nextErr := client.Next(nextCtx)

	cancel()

	if !errors.Is(nextErr, dbus.ErrClosed) {
		t.Fatalf("Next after connection loss error = %v, want dbus.ErrClosed", nextErr)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close after connection loss: %v", err)
	}
}

func TestLogindRearmRejectsChangedPrivateBusOwnerBeforeAcquiringFD(t *testing.T) {
	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)
	manager := exportFakeLogind(t, service, &fakeLogindProperties{})
	t.Cleanup(manager.close)
	t.Cleanup(func() { _ = service.Close() })

	signalFeed := newBoundedSignalHandler()
	clientConn := connectPrivateBusWithSignals(t, address, signalFeed)

	client, err := openLogindOnConnection(t.Context(), clientConn, signalFeed)
	if err != nil {
		_ = clientConn.Close()

		t.Fatalf("openLogindOnConnection failed: %v", err)
	}

	t.Cleanup(func() { _ = client.Close() })

	if err := client.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	replacement := connectPrivateBus(t, address)
	t.Cleanup(func() { _ = replacement.Close() })

	if _, err := service.ReleaseName(LogindBusName); err != nil {
		t.Fatalf("release fake logind name: %v", err)
	}

	if reply, err := replacement.RequestName(LogindBusName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request replacement logind name: reply=%v err=%v", reply, err)
	}

	nextCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	_, ownerErr := client.Next(nextCtx)

	cancel()

	if !errors.Is(ownerErr, ErrLogindOwnerChanged) {
		t.Fatalf("Next after owner loss error = %v, want ErrLogindOwnerChanged", ownerErr)
	}

	if err := client.Rearm(t.Context()); !errors.Is(err, ErrLogindOwnerChanged) {
		t.Fatalf("Rearm after owner loss error = %v, want ErrLogindOwnerChanged", err)
	}

	if got := manager.readerCount(); got != logindInhibitorCount {
		t.Fatalf("inhibitors acquired = %d, want no acquisition after owner loss", got)
	}
}

func TestOpenLogindClosesFirstInhibitorWhenOwnerChangesBetweenAcquisitions(t *testing.T) {
	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)
	manager := exportFakeLogind(t, service, &fakeLogindProperties{})
	t.Cleanup(manager.close)
	t.Cleanup(func() { _ = service.Close() })

	replacement := connectPrivateBus(t, address)
	t.Cleanup(func() { _ = replacement.Close() })

	firstAcquired := make(chan struct{})
	ownerChanged := make(chan struct{})

	manager.configure(0, func(call int) {
		if call != 1 {
			return
		}

		close(firstAcquired)
		<-ownerChanged
	})

	signalFeed := newBoundedSignalHandler()
	clientConn := connectPrivateBusWithSignals(t, address, signalFeed)

	type openResult struct {
		client *Logind
		err    error
	}

	opened := make(chan openResult, 1)

	go func() {
		client, err := openLogindOnConnection(t.Context(), clientConn, signalFeed)
		opened <- openResult{client: client, err: err}
	}()

	<-firstAcquired

	if _, err := service.ReleaseName(LogindBusName); err != nil {
		close(ownerChanged)
		t.Fatalf("release fake logind name: %v", err)
	}

	if reply, err := replacement.RequestName(LogindBusName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		close(ownerChanged)
		t.Fatalf("request replacement logind name: reply=%v err=%v", reply, err)
	}

	close(ownerChanged)

	result := <-opened
	if result.client != nil || !errors.Is(result.err, ErrLogindOwnerChanged) {
		_ = clientConn.Close()

		t.Fatalf("openLogindOnConnection = (%v, %v), want ErrLogindOwnerChanged", result.client, result.err)
	}

	assertInhibitorRequests(t, manager.requestsSnapshot(), []InhibitorSpec{ShutdownInhibitor()})
	closeManagerWriters(t, manager)
	assertReaderClosed(t, manager.reader(0))

	if err := clientConn.Close(); err != nil {
		t.Fatalf("close client connection: %v", err)
	}
}

func TestLogindRearmDoesNotPublishPartialInhibitorPair(t *testing.T) {
	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)
	manager := exportFakeLogind(t, service, &fakeLogindProperties{})
	t.Cleanup(manager.close)
	t.Cleanup(func() { _ = service.Close() })

	signalFeed := newBoundedSignalHandler()
	clientConn := connectPrivateBusWithSignals(t, address, signalFeed)

	client, err := openLogindOnConnection(t.Context(), clientConn, signalFeed)
	if err != nil {
		_ = clientConn.Close()

		t.Fatalf("openLogindOnConnection failed: %v", err)
	}

	t.Cleanup(func() { _ = client.Close() })

	if err := client.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	manager.configure(4, nil)

	if err := client.Rearm(t.Context()); err == nil {
		t.Fatal("Rearm succeeded after the sleep inhibitor acquisition failed")
	}

	client.mu.Lock()
	held := client.inhibitors
	client.mu.Unlock()

	if held[0] != nil || held[1] != nil {
		t.Fatalf("published partial inhibitor pair: %+v", held)
	}

	if err := client.Release(); !errors.Is(err, ErrLogindNotInhibited) {
		t.Fatalf("Release after partial Rearm error = %v, want ErrLogindNotInhibited", err)
	}

	closeManagerWriters(t, manager)
	assertReaderClosed(t, manager.reader(2))
}

func TestLogindReleaseAndCloseAttemptBothInhibitorCloses(t *testing.T) {
	tests := []struct {
		name         string
		operation    string
		precloseBoth bool
	}{
		{name: "release attempts sleep after shutdown failure", operation: "release", precloseBoth: false},
		{name: "close attempts sleep after shutdown failure", operation: "close", precloseBoth: false},
		{name: "release joins both failures", operation: "release", precloseBoth: true},
		{name: "close joins both failures", operation: "close", precloseBoth: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testLogindCloseAttemptsBoth(t, test.operation, test.precloseBoth)
		})
	}
}

func testLogindCloseAttemptsBoth(t *testing.T, operation string, precloseBoth bool) {
	t.Helper()

	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)
	manager := exportFakeLogind(t, service, &fakeLogindProperties{})
	t.Cleanup(manager.close)
	t.Cleanup(func() { _ = service.Close() })

	signalFeed := newBoundedSignalHandler()
	clientConn := connectPrivateBusWithSignals(t, address, signalFeed)

	client, err := openLogindOnConnection(t.Context(), clientConn, signalFeed)
	if err != nil {
		_ = clientConn.Close()

		t.Fatalf("openLogindOnConnection failed: %v", err)
	}

	if err := client.inhibitors[0].Close(); err != nil {
		t.Fatalf("preclose shutdown inhibitor: %v", err)
	}

	if precloseBoth {
		if err := client.inhibitors[1].Close(); err != nil {
			t.Fatalf("preclose sleep inhibitor: %v", err)
		}
	}

	closeManagerWriters(t, manager)

	if operation == "release" {
		err = client.Release()
	} else {
		err = client.Close()
	}

	if err == nil {
		t.Fatalf("%s succeeded despite shutdown inhibitor close failure", operation)
	}

	if precloseBoth && (!strings.Contains(err.Error(), "release shutdown logind delay inhibitor") ||
		!strings.Contains(err.Error(), "release sleep logind delay inhibitor")) {
		t.Fatalf("%s error did not retain both close failures: %v", operation, err)
	}

	assertReaderClosed(t, manager.reader(0))
	assertReaderClosed(t, manager.reader(1))

	if operation == "release" {
		if err := client.Close(); err != nil {
			t.Fatalf("Close after Release: %v", err)
		}
	}
}

func TestOpenLogindReleasesInhibitorWhenStartupIsAlreadyPreparing(t *testing.T) {
	address := startPrivateBus(t)
	service := connectPrivateBus(t, address)
	manager := exportFakeLogind(t, service, &fakeLogindProperties{preparingShutdown: true})
	t.Cleanup(manager.close)
	t.Cleanup(func() { _ = service.Close() })

	signalFeed := newBoundedSignalHandler()
	clientConn := connectPrivateBusWithSignals(t, address, signalFeed)

	client, err := openLogindOnConnection(t.Context(), clientConn, signalFeed)
	if client != nil || !errors.Is(err, ErrLogindPreparing) {
		_ = clientConn.Close()

		t.Fatalf("openLogindOnConnection = (%v, %v), want ErrLogindPreparing", client, err)
	}

	if err := manager.closeWriters(); err != nil {
		t.Fatalf("close service inhibitor descriptors: %v", err)
	}

	assertReaderClosed(t, manager.reader(0))
	assertReaderClosed(t, manager.reader(1))

	if err := clientConn.Close(); err != nil {
		t.Fatalf("close client connection: %v", err)
	}
}

func assertReaderClosed(t *testing.T, reader *os.File) {
	t.Helper()

	if reader == nil {
		t.Fatal("fake logind did not retain the inhibitor read descriptor")
	}

	closed := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1)
		_, readErr := reader.Read(buffer)

		closed <- readErr
	}()

	select {
	case readErr := <-closed:
		if !errors.Is(readErr, io.EOF) {
			t.Fatalf("inhibitor read after cleanup = %v, want EOF", readErr)
		}
	case <-time.After(time.Second):
		t.Fatal("inhibitor descriptor remained open after cleanup")
	}
}

func assertInhibitorRequests(t *testing.T, got, want []InhibitorSpec) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Fatalf("inhibitor requests = %+v, want %+v", got, want)
	}
}

func closeManagerWriters(t *testing.T, manager *fakeLogindManager) {
	t.Helper()

	if err := manager.closeWriters(); err != nil {
		t.Fatalf("close service inhibitor descriptors: %v", err)
	}
}

func startPrivateBus(t *testing.T) string {
	t.Helper()

	dbusDaemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		if os.Getenv("SESH_LIFECYCLE_INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("dbus-daemon is required: %v", err)
		}

		t.Skip("dbus-daemon is unavailable")
	}

	ctx, cancel := context.WithCancel(t.Context())
	command := exec.CommandContext(ctx, dbusDaemon, "--session", "--nofork", "--print-address=1")

	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe failed: %v", err)
	}

	if err := command.Start(); err != nil {
		t.Fatalf("start private dbus-daemon: %v", err)
	}

	t.Cleanup(func() {
		cancel()

		if waitErr := command.Wait(); waitErr != nil && !errors.Is(waitErr, context.Canceled) {
			var exitErr *exec.ExitError
			if !errors.As(waitErr, &exitErr) || exitErr.ProcessState.Success() {
				t.Errorf("wait for private dbus-daemon: %v", waitErr)
			}
		}
	})

	address, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read private bus address: %v", err)
	}

	address = address[:len(address)-1]

	return address
}

func connectPrivateBus(t *testing.T, address string) *dbus.Conn {
	t.Helper()

	service, err := dbus.Connect(address)
	if err != nil {
		t.Fatalf("connect private bus: %v", err)
	}

	return service
}

func connectPrivateBusWithSignals(t *testing.T, address string, signalFeed *boundedSignalHandler) *dbus.Conn {
	t.Helper()

	service, err := dbus.Connect(address, dbus.WithSignalHandler(signalFeed))
	if err != nil {
		t.Fatalf("connect private bus with bounded signal handler: %v", err)
	}

	return service
}

func exportFakeLogind(t *testing.T, service *dbus.Conn, properties *fakeLogindProperties) *fakeLogindManager {
	t.Helper()

	manager := new(fakeLogindManager)

	path := dbus.ObjectPath(LogindObjectPath)
	if err := service.Export(manager, path, LogindManagerIface); err != nil {
		t.Fatalf("export fake logind manager: %v", err)
	}

	if err := service.Export(properties, path, "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatalf("export fake logind properties: %v", err)
	}

	if reply, err := service.RequestName(LogindBusName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("request fake logind name: reply=%v err=%v", reply, err)
	}

	return manager
}
