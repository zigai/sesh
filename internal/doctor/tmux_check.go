//go:build linux || darwin

package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/session"
)

const (
	minTmuxMajor = 3
	minTmuxMinor = 6
	tmuxRequired = "3.6"
	upgradeTmux  = "Install tmux " + tmuxRequired + " or later"
)

func (s *Service) checkTmux(ctx context.Context) ([]Result, error) {
	client, clientOK := s.checkTmuxClient(ctx)
	if err := ctx.Err(); err != nil {
		return []Result{client}, fmt.Errorf("check tmux: %w", err)
	}

	if !clientOK {
		return []Result{client, skipped(CheckTmuxServer, CheckTmuxClient, "tmux is unusable")}, nil
	}

	server := s.checkTmuxServer(ctx)
	s.serverStatus = server.Status

	switch {
	case server.Status == StatusOK:
		s.sessions, s.listErr = s.listSessions(ctx)
	case server.Reason == ReasonNotRunning:
		server = s.withRestorableSessions(ctx, server)
	}

	if err := ctx.Err(); err != nil {
		return []Result{client, server}, fmt.Errorf("check tmux: %w", err)
	}

	return []Result{client, server}, nil
}

func (s *Service) checkTmuxClient(ctx context.Context) (Result, bool) {
	path, found := lookPath(s.opts.PathEnv, "tmux")
	if !found {
		return newResult(CheckTmuxClient, StatusFail, "tmux is not on PATH").
			withReason(ReasonNotFound).
			withManual(upgradeTmux), false
	}

	output, err := exec.CommandContext(ctx, path, "-V").Output()
	if err != nil {
		return newResult(CheckTmuxClient, StatusFail, fmt.Sprintf("%s -V failed: %v", DisplayPath(path), err)).
			withReason(ReasonInspectionFailed).
			withManual("Reinstall tmux " + tmuxRequired + " or later"), false
	}

	version := gotmux.ParseVersion(strings.TrimSpace(string(output)))
	if !version.AtLeast(minTmuxMajor, minTmuxMinor) {
		return newResult(CheckTmuxClient, StatusFail, fmt.Sprintf("%s at %s is too old; sesh needs %s or later", version, DisplayPath(path), tmuxRequired)).
			withReason(ReasonIncompatible).
			withManual(upgradeTmux), false
	}

	return newResult(CheckTmuxClient, StatusOK, fmt.Sprintf("%s at %s", version, DisplayPath(path))), true
}

func (s *Service) checkTmuxServer(ctx context.Context) Result {
	socket := DisplayPath(s.opts.Socket)

	if s.opts.ServerError != nil || s.opts.Server == nil {
		return newResult(CheckTmuxServer, StatusFail, fmt.Sprintf("Cannot use socket %s: %v", socket, s.opts.ServerError)).
			withReason(ReasonInvalid).
			withManual("Pass an absolute path to --socket")
	}

	info, err := s.opts.Server.Probe(ctx)
	if err != nil {
		return s.classifyTmuxProbeError(err)
	}

	s.serverPID = info.Identity.PID

	if !info.Version.AtLeast(minTmuxMajor, minTmuxMinor) {
		return newResult(CheckTmuxServer, StatusFail, fmt.Sprintf("Server runs tmux %s; sesh needs %s or later", info.Version, tmuxRequired)).
			withReason(ReasonIncompatible).
			withManual(upgradeTmux + ", then restart the tmux server")
	}

	return newResult(CheckTmuxServer, StatusOK, fmt.Sprintf("tmux %s, pid %d", info.Version, info.Identity.PID))
}

func (s *Service) classifyTmuxProbeError(err error) Result {
	socket := DisplayPath(s.opts.Socket)
	findServer := fmt.Sprintf("Find the tmux server for %s with 'pgrep -a tmux' and check whether it is stopped or busy", socket)

	switch {
	case errors.Is(err, gotmux.ErrNoServer):
		return newResult(CheckTmuxServer, StatusFail, "No server is listening on "+socket).
			withReason(ReasonNotRunning).
			withCommand(s.startServerCommand())
	case errors.Is(err, context.DeadlineExceeded):
		return newResult(CheckTmuxServer, StatusFail, "Server on "+socket+" did not answer in time").
			withReason(ReasonTimedOut).
			withManual(findServer)
	case errors.Is(err, context.Canceled):
		return newResult(CheckTmuxServer, StatusFail, "Probe of "+socket+" was canceled").
			withReason(ReasonCanceled).
			withCommand(s.doctorCommand())
	case errors.Is(err, os.ErrPermission):
		return newResult(CheckTmuxServer, StatusFail, "Permission denied on "+socket).
			withReason(ReasonUnsafe).
			withManual("Make " + socket + " and its directory owned by you")
	}

	if unsupported, ok := errors.AsType[*gotmux.UnsupportedError](err); ok {
		return newResult(CheckTmuxServer, StatusFail, fmt.Sprintf("Server runs tmux %s; sesh needs %s or later", unsupported.Version, tmuxRequired)).
			withReason(ReasonIncompatible).
			withManual(upgradeTmux + ", then restart the tmux server")
	}

	return newResult(CheckTmuxServer, StatusFail, fmt.Sprintf("Cannot query the server on %s: %v", socket, err)).
		withReason(ReasonInspectionFailed).
		withManual(findServer)
}

func (s *Service) startServerCommand() string {
	if s.opts.Socket == s.opts.DefaultSocket {
		return "tmux new-session -d"
	}

	return "tmux -S " + shellPath(s.opts.Socket) + " new-session -d"
}

func (s *Service) listSessions(ctx context.Context) ([]string, error) {
	sessions, err := session.ListLive(ctx, s.opts.Server, "")
	if err != nil {
		return nil, fmt.Errorf("list live sessions: %w", err)
	}

	names := make([]string, 0, len(sessions))
	for _, live := range sessions {
		names = append(names, live.Name)
	}

	return names, nil
}

func (s *Service) saveFix(result Result) Result {
	if len(s.sessions) == 0 {
		return result.withManual("Start a tmux session, then save it with 'sesh snapshot save <session>'")
	}

	return result.withCommand(s.seshCommand("snapshot save", []string{flagSocket, flagRepository, flagHistory}, s.sessions...))
}

func (s *Service) checkSmoke() Result {
	if s.serverStatus != StatusOK {
		return skipped(CheckSmokeSessions, CheckTmuxServer, "tmux server is unavailable")
	}

	if s.listErr != nil {
		return newResult(CheckSmokeSessions, StatusFail, "Listing live sessions failed: "+s.listErr.Error()).
			withReason(ReasonInspectionFailed).
			withCommand(s.seshCommand("session list", []string{flagSocket}))
	}

	return newResult(CheckSmokeSessions, StatusOK, "Listed "+plural(len(s.sessions), "live session"))
}
