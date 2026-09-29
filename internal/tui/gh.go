package tui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/display/layout"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui/providers"
)

const (
	ghCommandBudget = 3 * time.Second
	ghScanBudget    = 5 * time.Second
	ghOutputLimit   = 64 * 1024
)

type ghTickMsg struct{}

type ghResultMsg struct {
	request uint64
	entries map[ghKey]ghEntry
	rows    providers.Rows
}

type ghKey struct {
	repo   string
	branch string
}

type ghEntry struct {
	at time.Time
	pr layout.Cell
	ci layout.Cell
}

type ghTarget struct {
	paneID    string
	key       ghKey
	directory string
	remote    bool
	hasRemote bool
}

type ghPullRequest struct {
	Number int       `json:"number"`
	Checks []ghCheck `json:"statusCheckRollup"`
}

type ghCheck struct {
	Conclusion string `json:"conclusion"`
	Status     string `json:"status"`
	State      string `json:"state"`
}

func ghCell(text, role string, state layout.State) layout.Cell {
	return layout.Cell{Text: text, Role: role, Variants: nil, State: state}
}

func (m *Model) ghEnabled() bool {
	definition, _ := providers.DefinitionFor(providers.GH)
	return agentSummariesEnabled(m.config, m.deps) && m.sessionFormats.agentsAgent.UsesAny(definition.Fields...)
}

func (m *Model) ghTickCommand() tea.Cmd {
	if !m.ghEnabled() {
		return nil
	}

	definition, _ := providers.DefinitionFor(providers.GH)

	return tea.Tick(definition.Cadence.Interval, func(time.Time) tea.Msg { return ghTickMsg{} })
}

func (m *Model) loadGHCommand() tea.Cmd {
	if !m.ghEnabled() || m.providerRunner.InFlight(providers.GH) {
		return m.ghTickCommand()
	}

	targets := m.ghTargets()
	if len(targets) == 0 {
		return m.ghTickCommand()
	}

	ctx, request, ok := m.providerRunner.Begin(m.operationContext(), providers.GH)
	if !ok {
		return m.ghTickCommand()
	}

	previous := m.ghEntries

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		entries, rows := scanGH(ctx, targets, previous, time.Now())

		return ghResultMsg{request: request, entries: entries, rows: rows}
	}
}

func (m *Model) handleGH(msg ghResultMsg) tea.Cmd {
	if m.providerRunner.Complete(providers.GH, msg.request, msg.rows) {
		m.ghEntries = msg.entries
	}

	return m.ghTickCommand()
}

func (m *Model) ghTargets() []ghTarget {
	targets := make([]ghTarget, 0)

	for _, row := range m.displaySessions {
		remote := row.Origin == session.OriginSSH || row.Origin == session.OriginBridge
		for _, agent := range row.Agents {
			if agent.PaneID == "" {
				continue
			}

			targets = append(targets, ghTarget{
				paneID: agent.PaneID, key: ghKey{repo: agent.GitRepo, branch: agent.GitBranch},
				directory: agentGitDirectory(agent), remote: remote, hasRemote: agent.GitHasRemote,
			})
		}
	}

	return targets
}

func scanGH(ctx context.Context, targets []ghTarget, previous map[ghKey]ghEntry, now time.Time) (map[ghKey]ghEntry, providers.Rows) {
	ghPath, lookupErr := exec.LookPath("gh")

	return scanGHWithQuery(ctx, targets, previous, now, lookupErr == nil, func(ctx context.Context, target ghTarget, at time.Time) ghEntry {
		return queryGH(ctx, ghPath, target, at)
	})
}

func scanGHWithQuery(ctx context.Context, targets []ghTarget, previous map[ghKey]ghEntry, now time.Time, available bool, query func(context.Context, ghTarget, time.Time) ghEntry) (map[ghKey]ghEntry, providers.Rows) {
	ctx, cancel := context.WithTimeout(ctx, ghScanBudget)
	defer cancel()

	entries := make(map[ghKey]ghEntry)
	rows := make(providers.Rows, len(targets))

	for _, target := range targets {
		entry, cacheable := ghEntryForTarget(ctx, target, entries, previous, now, available, query)
		if cacheable {
			entries[target.key] = entry
		}

		rows[target.paneID] = map[string]layout.Cell{display.FieldAgentPR: entry.pr, display.FieldAgentCI: entry.ci}
	}

	return entries, rows
}

func ghEntryForTarget(ctx context.Context, target ghTarget, entries, previous map[ghKey]ghEntry, now time.Time, available bool, query func(context.Context, ghTarget, time.Time) ghEntry) (ghEntry, bool) {
	entry := ghEntry{at: time.Time{}, pr: ghCell("", "", layout.Absent), ci: ghCell("", "", layout.Absent)}
	if target.remote || !available || !target.hasRemote {
		entry.pr.State, entry.ci.State = layout.Unsupported, layout.Unsupported
		return entry, false
	}

	if target.key.repo == "" || target.key.branch == "" || target.directory == "" {
		return entry, false
	}

	if ctx.Err() != nil {
		entry.pr.State, entry.ci.State = layout.Pending, layout.Pending
		return entry, false
	}

	if cached, ok := entries[target.key]; ok {
		return cached, true
	}

	if cached, ok := previous[target.key]; ok && now.Sub(cached.at) < time.Minute {
		return cached, true
	}

	return query(ctx, target, now), true
}

func queryGH(ctx context.Context, ghPath string, target ghTarget, now time.Time) ghEntry {
	entry := ghEntry{at: now, pr: ghCell("", "", layout.Absent), ci: ghCell("", "", layout.Absent)}

	commandCtx, cancel := context.WithTimeout(ctx, ghCommandBudget)
	defer cancel()

	command := exec.CommandContext(commandCtx, ghPath, "pr", "list", "--head", target.key.branch, "--json", "number,statusCheckRollup", "--limit", "1")
	command.Dir = target.directory

	command.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1")
	command.WaitDelay = gitWaitDelay
	output := &boundedBuffer{data: nil, limit: ghOutputLimit}

	command.Stdout = output
	if err := command.Run(); err != nil {
		entry.pr.State, entry.ci.State = layout.Pending, layout.Pending
		return entry
	}

	var results []ghPullRequest
	if err := json.Unmarshal([]byte(output.String()), &results); err != nil {
		entry.pr.State, entry.ci.State = layout.Pending, layout.Pending
		return entry
	}

	if len(results) == 0 || results[0].Number <= 0 {
		return entry
	}

	result := results[0]
	entry.pr = ghCell("#"+strconv.Itoa(result.Number), "", layout.Present)
	entry.ci = classifyGHChecks(result.Checks)

	return entry
}

func classifyGHChecks(checks []ghCheck) layout.Cell {
	if len(checks) == 0 {
		return ghCell("", "", layout.Absent)
	}

	pending := false

	for _, check := range checks {
		if ghCheckFailed(check) {
			return ghCell("failing", "error", layout.Present)
		}

		if ghCheckPending(check) {
			pending = true
		}
	}

	if pending {
		return ghCell("pending", "warn", layout.Present)
	}

	return ghCell("passing", "success", layout.Present)
}

func ghCheckFailed(check ghCheck) bool {
	switch strings.ToUpper(check.State) {
	case "FAILURE", "ERROR":
		return true
	}

	switch strings.ToUpper(check.Conclusion) {
	case "FAILURE", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return true
	}

	return strings.HasPrefix(strings.ToUpper(check.Conclusion), "CANCEL")
}

func ghCheckPending(check ghCheck) bool {
	state := strings.ToUpper(check.State)
	if state == "SUCCESS" || state == "EXPECTED" {
		return false
	}

	return strings.ToUpper(check.Status) != "COMPLETED"
}
