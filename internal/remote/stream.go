//go:build linux || darwin

package remote

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"errors"
	"io"
	"math/big"
	"os/exec"
	"sync"
	"time"

	"github.com/samber/oops"

	"github.com/zigai/sesh/internal/config"
)

const (
	streamBackoffMultiplier = 2
	streamJitterDivisor     = 2

	streamBackoffMin = time.Second
	streamBackoffMax = 30 * time.Second
	streamFrameGrace = 45 * time.Second
)

var errNilStreamYield = errors.New("remote stream yield callback is not configured")

type StreamOptions struct {
	Remote       config.RemoteConfig
	Connection   config.RemoteConnectionConfig
	Bootstrap    StreamBootstrap
	Command      func(ctx context.Context, executable string, arguments ...string) *exec.Cmd
	Yield        func(frame Frame, streamErr error, attempted bool) error
	HelloTimeout time.Duration
	FrameTimeout time.Duration
}

type StreamIdentity struct {
	PeerID         string
	StreamInstance string
	Sequence       uint64
}

func (identity *StreamIdentity) Accept(frame Frame) error {
	if identity.Sequence == 0 {
		if frame.Type != FrameHello {
			return ErrInvalidFrame
		}

		identity.PeerID = frame.PeerID
		identity.StreamInstance = frame.StreamInstance
		identity.Sequence = frame.Sequence

		return nil
	}

	if frame.Type != FrameSnapshot && frame.Type != FrameHealth {
		return ErrInvalidFrame
	}

	if frame.PeerID != identity.PeerID || frame.StreamInstance != identity.StreamInstance || frame.Sequence <= identity.Sequence {
		return ErrInvalidFrame
	}

	identity.Sequence = frame.Sequence

	return nil
}

func StreamBootstrapFor(remote config.RemoteConfig, connection config.RemoteConnectionConfig) StreamBootstrap {
	return StreamBootstrap{
		Version:           ProtocolVersion,
		TmuxSocket:        connection.TmuxSocket,
		PollInterval:      remote.TmuxPollInterval,
		HeartbeatInterval: remote.HeartbeatInterval,
	}
}

func Stream(ctx context.Context, opts StreamOptions) error {
	if opts.Yield == nil {
		return oops.In("remote").With("operation", "stream").Wrap(errNilStreamYield)
	}

	backoff := streamBackoffMin

	for ctx.Err() == nil {
		if err := opts.Yield(Frame{
			Version:        0,
			Type:           "",
			PeerID:         "",
			StreamInstance: "",
			Sequence:       0,
			Hello:          nil,
			Snapshot:       nil,
			Health:         nil,
		}, nil, true); err != nil {
			return err
		}

		var pending sync.WaitGroup

		streamErr, acceptedSnapshot := runStreamAttempt(ctx, opts, &pending)

		if ctx.Err() != nil {
			pending.Wait()

			return oops.In("remote").With("operation", "stream").Wrapf(ctx.Err(), "running remote stream")
		}

		if acceptedSnapshot {
			backoff = streamBackoffMin
		}

		yieldErr := opts.Yield(Frame{
			Version:        0,
			Type:           "",
			PeerID:         "",
			StreamInstance: "",
			Sequence:       0,
			Hello:          nil,
			Snapshot:       nil,
			Health:         nil,
		}, streamErr, false)

		pending.Wait()

		if yieldErr != nil {
			return yieldErr
		}

		delay, jitterErr := retryDelay(backoff)
		if jitterErr != nil {
			return jitterErr
		}

		timer := time.NewTimer(delay)

		select {
		case <-ctx.Done():
			timer.Stop()

			return oops.In("remote").With("operation", "stream").Wrapf(ctx.Err(), "running remote stream")
		case <-timer.C:
		}

		backoff = min(backoff*streamBackoffMultiplier, streamBackoffMax)
	}

	return oops.In("remote").With("operation", "stream").Wrapf(ctx.Err(), "running remote stream")
}

func runStreamAttempt(ctx context.Context, opts StreamOptions, pending *sync.WaitGroup) (error, bool) {
	attemptContext, cancelAttempt := context.WithCancel(ctx)
	defer cancelAttempt()

	bootstrap, err := EncodeStreamBootstrap(opts.Bootstrap)
	if err != nil {
		return err, false
	}

	command, cmdErr := NewSSHCommand(attemptContext, opts.Remote, opts.Connection, SSHCommandOptions{
		Interactive: false,
		Forward:     nil,
		Command:     opts.Command,
	}, PeerEntryPoint(opts.Connection, ChannelStream)...)
	if cmdErr != nil {
		return cmdErr, false
	}

	command.Stdin = bytes.NewReader(bootstrap)

	stdout, err := command.StdoutPipe()
	if err != nil {
		return ErrConnectionUnavailable, false
	}

	diagnostics := NewDiagnosticsBuffer(maxDiagnosticsBytes)

	command.Stderr = diagnostics

	if err := command.Start(); err != nil {
		_ = stdout.Close()

		return ErrConnectionUnavailable, false
	}

	helloAccepted := make(chan struct{})

	var helloOnce sync.Once

	markReady := func() {
		helloOnce.Do(func() { close(helloAccepted) })
	}

	helloExpired := watchHelloDeadline(attemptContext, helloAccepted, cancelAttempt,
		positiveDuration(opts.HelloTimeout, defaultConnect))

	streamErr, acceptedSnapshot := consumeFrames(attemptContext, NewFrameReader(stdout), stdout, opts.Yield, markReady,
		positiveDuration(opts.FrameTimeout, streamFrameGrace), pending)

	finishErr := FinishCommand(ctx, command, stdout, diagnostics)

	select {
	case <-helloExpired:
		return context.DeadlineExceeded, acceptedSnapshot
	default:
	}

	if finishErr != nil {
		return errors.Join(streamErr, finishErr), acceptedSnapshot
	}

	return streamErr, acceptedSnapshot
}

func watchHelloDeadline(ctx context.Context, accepted <-chan struct{}, cancel context.CancelFunc, timeout time.Duration) <-chan struct{} {
	expired := make(chan struct{})

	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case <-timer.C:
			close(expired)
			cancel()
		case <-accepted:
		case <-ctx.Done():
		}
	}()

	return expired
}

func consumeFrames(
	ctx context.Context,
	reader *FrameReader,
	stream io.Closer,
	yield func(Frame, error, bool) error,
	ready func(),
	frameTimeout time.Duration,
	pending *sync.WaitGroup,
) (error, bool) {
	var (
		identity         StreamIdentity
		acceptedSnapshot bool
	)

	for {
		frame, err := readFrameWithTimeout(ctx, reader, stream, frameTimeout)
		if err != nil {
			return err, acceptedSnapshot
		}

		if err := identity.Accept(frame); err != nil {
			return err, acceptedSnapshot
		}

		result := make(chan error, 1)

		pending.Go(func() {
			result <- yield(frame, nil, false)
		})

		select {
		case acceptErr := <-result:
			if acceptErr != nil {
				return acceptErr, acceptedSnapshot
			}
		case <-ctx.Done():
			return oops.In("remote").With("operation", "accept_stream_frame").Wrap(ctx.Err()), acceptedSnapshot
		}

		if frame.Type == FrameHello && ready != nil {
			ready()
		}

		if frame.Type == FrameSnapshot {
			acceptedSnapshot = true
		}
	}
}

func readFrameWithTimeout(ctx context.Context, reader *FrameReader, stream io.Closer, timeout time.Duration) (Frame, error) {
	type readResult struct {
		frame Frame
		err   error
	}

	result := make(chan readResult, 1)

	go func() {
		frame, err := reader.Read()
		result <- readResult{frame: frame, err: err}
	}()

	timer := time.NewTimer(positiveDuration(timeout, streamFrameGrace))
	defer timer.Stop()

	select {
	case <-ctx.Done():
		_ = stream.Close()

		return Frame{}, oops.In("remote").With("operation", "read_stream_frame").Wrap(ctx.Err())
	case <-timer.C:
		_ = stream.Close()

		<-result

		return Frame{}, context.DeadlineExceeded
	case read := <-result:
		return read.frame, read.err
	}
}

func retryDelay(backoff time.Duration) (time.Duration, error) {
	bound := big.NewInt(max(int64(backoff/streamJitterDivisor), 1))

	value, err := cryptorand.Int(cryptorand.Reader, bound)
	if err != nil {
		return 0, oops.In("remote").With("operation", "retry_jitter").Wrap(err)
	}

	return backoff + time.Duration(value.Int64()), nil
}
