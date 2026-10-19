package task

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"syscall"
	"time"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/snapshot/apps"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	activityIdle = "idle"

	stopWait     = 10 * time.Second
	stopInterval = 100 * time.Millisecond
)

var (
	ErrNotFound         = errors.New("task is not in the task ledger")
	ErrWindowMissing    = errors.New("task window is not in tmux")
	ErrNotAwake         = errors.New("task agent is not awake")
	ErrNotAsleep        = errors.New("task agent is not asleep")
	ErrNoAgent          = errors.New("no tracked agent runs in the task's agent pane")
	ErrAgentBusy        = errors.New("agent is not idle")
	ErrNoResume         = errors.New("agent has no native resume evidence")
	ErrSleepUnsupported = errors.New("sleep is not verified for this harness")
	ErrAgentChanged     = errors.New("agent process changed before it could be stopped")
	ErrAgentDidNotStop  = errors.New("agent did not exit after the stop signal")
)

type Agent struct {
	Harness       string
	Activity      string
	SessionID     string
	SessionPath   string
	ResumeCommand []string
	Executable    string
	PanePID       int
}

type AgentFinder interface {
	AgentsIn(ctx context.Context, pane gotmux.PaneID) ([]Agent, error)
}

type sleepPlan struct {
	signal  syscall.Signal
	process *process.Observation
	resume  Resume
}

type Woken struct {
	Record       Record
	Command      []string
	AlreadyAwake bool
}

// Sleep records the resume evidence before the agent is signaled, so a sleep cut short never
// loses the way back.
func (s *Service) Sleep(ctx context.Context, id string) (Record, error) {
	record, live, err := s.task(ctx, id)
	if err != nil {
		return record, taskError("sleep_task", err)
	}

	if record.State != StateActive {
		return record, taskError("sleep_task", ErrNotAwake)
	}

	agent, err := s.agentIn(ctx, live)
	if err != nil {
		return record, taskError("sleep_task", err)
	}

	plan, err := planSleep(ctx, agent, live.AgentPID)
	if err != nil {
		return record, taskError("sleep_task", err)
	}

	record.State = StateSleeping
	record.Resume = plan.resume

	if err := s.put(ctx, record); err != nil {
		return record, taskError("sleep_task", err)
	}

	if delivered, err := stopAgent(ctx, plan.process.PID, plan.process.Birth, plan.signal); err != nil {
		if delivered {
			return record, taskError("sleep_task", err)
		}

		record.State = StateActive
		record.Resume = emptyResume()

		return record, taskError("sleep_task", errors.Join(err, s.put(context.WithoutCancel(ctx), record)))
	}

	if err := s.showPlaceholder(ctx, live.AgentPane, record); err != nil {
		return record, taskError("sleep_task", err)
	}

	return record, nil
}

func (s *Service) Wake(ctx context.Context, id string) (Woken, error) {
	return s.wake(ctx, id, func(live Live, woken Woken) error {
		return s.respawn(ctx, live.AgentPane, woken.Record, woken.Command)
	})
}

func (s *Service) WakeHere(ctx context.Context, id string) (Woken, error) {
	return s.wake(ctx, id, func(Live, Woken) error { return nil })
}

func (s *Service) wake(ctx context.Context, id string, relaunch func(Live, Woken) error) (Woken, error) {
	record, live, err := s.task(ctx, id)
	if err != nil {
		return Woken{Record: record, Command: nil, AlreadyAwake: false}, taskError("wake_task", err)
	}

	woken, err := s.beginWake(ctx, record, live)
	if err != nil || woken.AlreadyAwake {
		return woken, taskError("wake_task", err)
	}

	if err := relaunch(live, woken); err != nil {
		return woken, taskError("wake_task", fmt.Errorf("relaunch agent: %w", err))
	}

	woken.Record.State = StateActive

	return woken, taskError("wake_task", s.put(context.WithoutCancel(ctx), woken.Record))
}

func (s *Service) beginWake(ctx context.Context, record Record, live Live) (Woken, error) {
	woken := Woken{Record: record, Command: slices.Clone(record.Resume.Command), AlreadyAwake: false}

	if record.State != StateSleeping && record.State != StateWaking {
		return woken, ErrNotAsleep
	}

	if len(woken.Command) == 0 {
		return woken, ErrNoResume
	}

	running, err := harnessRunning(ctx, live.AgentPID, record.Resume.Harness)
	if err != nil {
		return woken, err
	}

	woken.Record.State = StateWaking
	if running {
		woken.Record.State = StateActive
		woken.AlreadyAwake = true
	}

	return woken, s.put(ctx, woken.Record)
}

func (s *Service) task(ctx context.Context, id string) (Record, Live, error) {
	record, err := s.record(ctx, id)
	if err != nil {
		return record, absent(), err
	}

	live, err := s.locate(ctx, record.ID)

	return record, live, err
}

func (s *Service) locate(ctx context.Context, id string) (Live, error) {
	located, err := locateTasks(ctx, s.server)
	if err != nil {
		return absent(), err
	}

	live, found := located[id]
	if !found || live.AgentPane == "" {
		return absent(), ErrWindowMissing
	}

	return live, nil
}

func (s *Service) agentIn(ctx context.Context, live Live) (Agent, error) {
	running, err := s.agents.AgentsIn(ctx, live.AgentPane)
	if err != nil {
		return emptyAgent(), fmt.Errorf("find task agent: %w", err)
	}

	running = slices.DeleteFunc(running, func(agent Agent) bool { return agent.PanePID != live.AgentPID })
	if len(running) != 1 {
		return emptyAgent(), ErrNoAgent
	}

	return running[0], nil
}

func (s *Service) showPlaceholder(ctx context.Context, pane gotmux.PaneID, record Record) error {
	if err := s.respawn(ctx, pane, record, s.sleepingAgentCommand(record.ID)); err != nil {
		return fmt.Errorf("show sleep placeholder: %w", err)
	}

	return nil
}

func (s *Service) respawn(ctx context.Context, paneID gotmux.PaneID, record Record, argv []string) error {
	shell, err := defaultShell(ctx, s.server)
	if err != nil {
		return err
	}

	pane, err := s.server.Pane(ctx, paneID)
	if err != nil {
		return fmt.Errorf("find task pane: %w", err)
	}

	if err := pane.Respawn(ctx, gotmux.RespawnOptions{
		Program: shellProgram(shell, argv), Dir: record.Directory, Env: nil, TmuxEnv: Env(record),
		KillRunning: true, PreserveEnvironment: false,
	}); err != nil {
		return fmt.Errorf("respawn task pane: %w", err)
	}

	return nil
}

func Env(record Record) map[string]string {
	if record.Kind == KindLoose {
		return map[string]string{EnvTask: record.ID}
	}

	return map[string]string{EnvTask: record.ID, EnvProject: record.Project, EnvWorktree: record.Directory, EnvBase: record.Base}
}

func planSleep(ctx context.Context, agent Agent, panePID int) (sleepPlan, error) {
	none := sleepPlan{signal: 0, process: nil, resume: emptyResume()}

	if agent.Activity != activityIdle {
		return none, fmt.Errorf("%w: it is %s", ErrAgentBusy, agent.Activity)
	}

	signal, ok := apps.StopSignal(agent.Harness)
	if !ok {
		return none, ErrSleepUnsupported
	}

	foreground, err := observeForeground(ctx, panePID)
	if err != nil {
		return none, errors.Join(ErrAgentChanged, err)
	}

	prepared, reason := apps.Prepare(observedLaunch(foreground), &apps.AgentResume{
		Harness: agent.Harness, SessionID: agent.SessionID, SessionPath: agent.SessionPath,
		Command: agent.ResumeCommand, Executable: agent.Executable,
	})
	if reason != apps.ReasonAgentResume || len(prepared.Arguments) == 0 {
		return none, fmt.Errorf("%w: %s", ErrNoResume, reason)
	}

	session := agent.SessionID
	if session == "" {
		session = agent.SessionPath
	}

	return sleepPlan{
		signal: signal, process: foreground,
		resume: Resume{Harness: agent.Harness, Session: session, Command: prepared.Command()},
	}, nil
}

func harnessRunning(ctx context.Context, panePID int, harness string) (bool, error) {
	foreground, err := observeForeground(ctx, panePID)
	if err != nil {
		return false, fmt.Errorf("inspect agent pane: %w", err)
	}

	return apps.RunsHarness(observedLaunch(foreground), harness), nil
}

func observeForeground(ctx context.Context, panePID int) (*process.Observation, error) {
	paneBirth, err := process.ReadIdentity(ctx, panePID)
	if err != nil {
		return nil, fmt.Errorf("read pane process: %w", err)
	}

	foreground, err := process.ObserveForeground(ctx, process.ForegroundRequest{
		PanePID: panePID, ExpectedPaneBirth: paneBirth, EnvironmentKeys: nil,
	})
	if err != nil {
		return nil, fmt.Errorf("observe pane foreground: %w", err)
	}

	return foreground, nil
}

func observedLaunch(observed *process.Observation) model.Launch {
	arguments := make([]model.Bytes, 0, len(observed.Arguments))
	for _, argument := range observed.Arguments {
		arguments = append(arguments, model.Bytes(argument))
	}

	return model.Launch{
		Executable: model.Bytes(observed.Executable), Arguments: arguments, Directory: model.Bytes(observed.Directory),
		Environment: observed.Environment, Birth: model.Bytes(observed.Birth.Bytes()),
		Recovery: model.RecoveryUnverified, Reason: "",
	}
}

func emptyResume() Resume {
	return Resume{Harness: "", Session: "", Command: []string{}}
}

func emptyAgent() Agent {
	return Agent{
		Harness: "", Activity: "", SessionID: "", SessionPath: "", ResumeCommand: nil, Executable: "", PanePID: 0,
	}
}

func stopAgent(ctx context.Context, pid int, identity process.Identity, signal syscall.Signal) (bool, error) {
	current, err := process.ReadIdentity(ctx, pid)
	if err != nil || current != identity {
		return false, errors.Join(ErrAgentChanged, err)
	}

	if err := syscall.Kill(pid, signal); err != nil {
		return false, fmt.Errorf("signal agent: %w", err)
	}

	ticker := time.NewTicker(stopInterval)
	defer ticker.Stop()

	deadline := time.NewTimer(stopWait)
	defer deadline.Stop()

	for {
		select {
		case <-ctx.Done():
			return true, fmt.Errorf("wait for agent exit: %w", ctx.Err())
		case <-deadline.C:
			// Never escalate: a harness killed outright may not have saved its session.
			return true, ErrAgentDidNotStop
		case <-ticker.C:
			current, err := process.ReadIdentity(ctx, pid)
			if errors.Is(err, process.ErrProcessVanished) || errors.Is(err, process.ErrProcessZombie) ||
				(err == nil && current != identity) {
				return true, nil
			}
		}
	}
}
