package tui

import (
	"errors"
	"runtime"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/zigai/sesh/internal/display"
	"github.com/zigai/sesh/internal/display/layout"
	"github.com/zigai/sesh/internal/session"
	"github.com/zigai/sesh/internal/tui/providers"
)

type resourceTickMsg struct{}

type resourceResultMsg struct {
	request uint64
	sample  providers.ResourceSample
	rows    providers.Rows
	err     error
}

func (m *Model) resourceEnabled() bool {
	definition, _ := providers.DefinitionFor(providers.Resource)
	return agentSummariesEnabled(m.config, m.deps) && m.sessionFormats.agentRowsUseAny(definition.Fields...)
}

func (m *Model) resourceTickCommand() tea.Cmd {
	if !m.resourceEnabled() || runtime.GOOS != "linux" {
		return nil
	}

	definition, _ := providers.DefinitionFor(providers.Resource)

	return tea.Tick(definition.Cadence.Interval, func(time.Time) tea.Msg { return resourceTickMsg{} })
}

func (m *Model) loadResourceCommand() tea.Cmd {
	if !m.resourceEnabled() || m.providerRunner.InFlight(providers.Resource) {
		return m.resourceTickCommand()
	}

	targets := m.resourceTargets()
	if len(targets) == 0 {
		return m.resourceTickCommand()
	}

	if runtime.GOOS != "linux" {
		var empty providers.ResourceSample
		m.providerRunner.Publish(providers.Resource, providers.ResourceRows(empty, empty, targets))

		return nil
	}

	ctx, request, ok := m.providerRunner.Begin(m.operationContext(), providers.Resource)
	if !ok {
		return m.resourceTickCommand()
	}

	previous := m.resourceSample

	procRoot := m.resourceProcRoot
	if procRoot == "" {
		procRoot = "/proc"
	}

	cachePath := resourceCachePath()

	return func() tea.Msg {
		if !m.lifecycle.BeginWorker() {
			return nil
		}
		defer m.lifecycle.EndWorker()

		if previous.At.IsZero() {
			previous = readResourceCache(cachePath)
		}

		sample, err := providers.ReadResourceSample(ctx, procRoot, time.Now())
		if err != nil {
			return resourceResultMsg{request: request, sample: providers.ResourceSample{At: time.Time{}, Processes: nil}, rows: nil, err: err}
		}

		rows := providers.ResourceRows(sample, previous, targets)
		if ctx.Err() == nil {
			_ = writeResourceCache(cachePath, sample)
		}

		return resourceResultMsg{request: request, sample: sample, rows: rows, err: nil}
	}
}

func (m *Model) handleResource(msg resourceResultMsg) tea.Cmd {
	if !m.providerRunner.InFlight(providers.Resource) || msg.request != m.providerRunner.CurrentRequest(providers.Resource) {
		return m.resourceTickCommand()
	}

	if msg.err != nil {
		if errors.Is(msg.err, providers.ErrResourceUnsupported) {
			rows := make(providers.Rows)
			for _, target := range m.resourceTargets() {
				fields := make(map[string]layout.Cell)
				for _, field := range display.FieldsByProvider(display.ProviderResource) {
					fields[field] = layout.Cell{Text: "", State: layout.Unsupported, Role: "", Variants: nil}
				}

				rows[target.PaneID] = fields
			}

			m.providerRunner.Complete(providers.Resource, msg.request, rows)
		} else {
			m.providerRunner.Invalidate(providers.Resource)
		}

		return m.resourceTickCommand()
	}

	if m.providerRunner.Complete(providers.Resource, msg.request, msg.rows) {
		m.resourceSample = msg.sample
	}

	return m.resourceTickCommand()
}

func (m *Model) resourceTargets() []providers.ResourceTarget {
	targets := make([]providers.ResourceTarget, 0)

	for _, row := range m.sessions {
		remote := row.Origin == session.OriginSSH || row.Origin == session.OriginBridge
		for _, agent := range row.Agents {
			if agent.PaneID == "" {
				continue
			}

			targets = append(targets, providers.ResourceTarget{
				PaneID: agent.PaneID, PanePID: agent.PanePID, WindowIndex: agent.WindowIndex,
				SessionKey: row.RuntimeKey(), Remote: remote,
			})
		}
	}

	return targets
}
