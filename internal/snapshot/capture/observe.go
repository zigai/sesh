package capture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/samber/oops"

	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/integration/process"
	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	maxCaptureAttempts = 3
	captureRetryDelay  = 10 * time.Millisecond
)

var beforeCompareGraphHook func()

type Observation struct {
	Manifest             model.Manifest
	Artifacts            map[string][]byte
	FloatingPanesOmitted int
}

type paneProcessRecord struct {
	pid        int
	birth      process.Identity
	foreground *process.Observation
	dead       bool
}

// OmittedTerminalText counts panes observed without a terminal text artifact because their
// text exceeded the capture byte budget.
func (o *Observation) OmittedTerminalText() int {
	omitted := 0

	for _, pane := range o.Manifest.Panes {
		if pane.Terminal.Artifact == "" {
			omitted++
		}
	}

	return omitted
}

func Observe(ctx context.Context, server *gotmux.Server, selected []gotmux.SessionID) (Observation, error) {
	observation, err := observe(ctx, server, selected, nil, false, nil)
	return observation, captureError(err)
}

func ObserveWithProcessEnrichment(ctx context.Context, server *gotmux.Server, selected []gotmux.SessionID, envKeys []string) (Observation, error) {
	if err := process.ValidateEnvironmentKeys(envKeys); err != nil {
		return Observation{}, captureError(err)
	}

	observation, err := observe(ctx, server, selected, envKeys, true, nil)

	return observation, captureError(err)
}

func filterProcessEnvKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(keys))

	var filtered []string

	for _, k := range keys {
		if k == "TMUX" || k == "TMUX_PANE" {
			continue
		}

		if seen[k] {
			continue
		}

		seen[k] = true
		filtered = append(filtered, k)
	}

	return filtered
}

func observe(ctx context.Context, server *gotmux.Server, selected []gotmux.SessionID, envKeys []string, enrichProcesses bool, agentSource agents.SessionSource) (Observation, error) {
	var lastErr error

	for attempt := range maxCaptureAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Observation{}, captureError(ctx.Err())
			case <-time.After(captureRetryDelay):
			}
		}

		obs, err := observeOnce(ctx, server, selected, envKeys, enrichProcesses, agentSource)
		if err == nil {
			return obs, nil
		}

		if !errors.Is(err, ErrChanged) {
			return Observation{}, err
		}

		lastErr = err
	}

	return Observation{}, lastErr
}

func observeOnce(ctx context.Context, server *gotmux.Server, selected []gotmux.SessionID, envKeys []string, enrichProcesses bool, agentSource agents.SessionSource) (Observation, error) {
	if len(selected) == 0 {
		return Observation{}, ErrSelection
	}

	if server == nil || server.Endpoint().SocketPath == "" {
		return Observation{}, tmuxserver.ErrEndpoint
	}

	started := time.Now().UnixNano()

	info, before, err := probeInitialSnapshot(ctx, server)
	if err != nil {
		return Observation{}, err
	}

	m, floating, err := tiledGraph(before, selected)
	if err != nil {
		return Observation{}, err
	}

	result := Observation{Manifest: m, Artifacts: make(map[string][]byte), FloatingPanesOmitted: floating}
	if err := result.collect(ctx, server, before); err != nil {
		return Observation{}, err
	}

	var paneProcesses map[string]paneProcessRecord

	if enrichProcesses {
		records, err := result.processes(ctx, before, envKeys, agentSource, server.Endpoint().SocketPath)
		if err != nil {
			return Observation{}, err
		}

		paneProcesses = records
	}

	if beforeCompareGraphHook != nil {
		beforeCompareGraphHook()
	}

	if err := compareGraph(ctx, server, before, selected, &result.Manifest, paneProcesses); err != nil {
		return Observation{}, err
	}

	if err := result.finalize(info, started, server.Endpoint().SocketPath, enrichProcesses); err != nil {
		return Observation{}, err
	}

	return result, nil
}

func (o *Observation) finalize(info gotmux.ServerInfo, started int64, endpoint string, enriched bool) error {
	o.Manifest.Format, o.Manifest.Version = "sesh", model.Schema
	o.Manifest.Started, o.Manifest.Finished = started, time.Now().UnixNano()
	o.Manifest.Source.TmuxVersion = fmt.Sprint(info.Version)
	o.Manifest.Source.LibraryVersion, o.Manifest.Source.Consistency = tmuxserver.LibraryVersion, "observed"

	endpointScope, err := model.EndpointScope(endpoint)
	if err != nil {
		return oops.In("snapshot.capture").
			Code("capture_failed").
			With("operation", "scope_endpoint", "reason", "endpoint_not_canonical").
			Wrap(err)
	}

	o.Manifest.Source.EndpointScope = endpointScope
	o.Manifest.Evidence = observationEvidence(enriched, o.FloatingPanesOmitted > 0)
	o.Manifest = o.Manifest.CompactInheritedOptions()

	if err := o.Manifest.Validate(); err != nil {
		return oops.In("snapshot.capture").With("operation", "validate_observation").Wrapf(err, "validate observation")
	}

	return nil
}

func (o *Observation) processes(ctx context.Context, snapshot gotmux.Snapshot, envKeys []string, source agents.SessionSource, endpoint string) (map[string]paneProcessRecord, error) {
	liveAgents, err := recoveryAgents(ctx, source)
	if err != nil {
		return nil, err
	}

	filteredKeys := filterProcessEnvKeys(envKeys)
	records := make(map[string]paneProcessRecord, len(o.Manifest.Panes))

	panes := make(map[string]gotmux.PaneInfo)
	for _, info := range snapshot.Panes() {
		panes[ref("p", string(info.ID))] = info
	}

	for i := range o.Manifest.Panes {
		paneID := o.Manifest.Panes[i].ID
		paneInfo, found := panes[paneID]

		if !found {
			return nil, ErrChanged
		}

		if paneInfo.Dead || paneInfo.PID <= 0 {
			o.Manifest.Panes[i].Dead = true
			o.Manifest.Panes[i].Launch = model.Launch{
				Executable:  nil,
				Arguments:   nil,
				Directory:   nil,
				Environment: nil,
				Birth:       nil,
				Recovery:    model.RecoveryNonrestartable,
				Reason:      string(model.ReasonPaneDead),
			}

			records[paneID] = paneProcessRecord{
				pid:        paneInfo.PID,
				birth:      process.Identity{BootID: "", StartTime: 0},
				foreground: nil,
				dead:       true,
			}

			continue
		}

		launch, record, err := captureProcessLaunch(ctx, paneInfo.PID, filteredKeys, matchingAgentResume(liveAgents, endpoint, paneInfo))
		if err != nil {
			return nil, err
		}

		o.Manifest.Panes[i].Launch, records[paneID] = launch, record
	}

	return records, nil
}

func (o *Observation) collect(ctx context.Context, server *gotmux.Server, snapshot gotmux.Snapshot) error {
	checks := []func() error{
		func() error { return o.globals(ctx, server) },
		func() error { return o.sessions(ctx, snapshot) },
		func() error { return o.windows(ctx, snapshot) },
		func() error { return o.panes(ctx, snapshot, server.Limits().OutputBytes) },
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}

	return nil
}

func (o *Observation) globals(ctx context.Context, server *gotmux.Server) error {
	for _, scoped := range []struct {
		name   string
		source optionLister
	}{
		{"server", server.Options()}, {"global-session", server.GlobalSessionOptions()}, {"global-window", server.GlobalWindowOptions()},
	} {
		values, err := options(ctx, scoped.name, scoped.source)
		if err != nil {
			return err
		}

		o.Manifest.Source.Options = append(o.Manifest.Source.Options, values...)
	}

	values, err := environment(ctx, server.Environment())
	if err != nil {
		return err
	}

	o.Manifest.Source.Environment = values

	return nil
}

func (o *Observation) sessions(ctx context.Context, snapshot gotmux.Snapshot) error {
	for _, info := range snapshot.Sessions() {
		index := slices.IndexFunc(o.Manifest.Sessions, func(s model.Session) bool { return s.ID == ref("s", string(info.ID)) })
		if index < 0 {
			continue
		}

		settings, err := options(ctx, "session", info.Handle().Options())
		if err != nil {
			return err
		}

		values, err := environment(ctx, info.Handle().Environment())
		if err != nil {
			return err
		}

		o.Manifest.Sessions[index].Options, o.Manifest.Sessions[index].Environment = settings, values
	}

	return nil
}

func (o *Observation) windows(ctx context.Context, snapshot gotmux.Snapshot) error {
	for _, info := range snapshot.Windows() {
		index := slices.IndexFunc(o.Manifest.Windows, func(w model.Window) bool { return w.ID == ref("w", string(info.ID)) })
		if index < 0 {
			continue
		}

		settings, err := options(ctx, "window", info.Handle().Options())
		if err != nil {
			return err
		}

		o.Manifest.Windows[index].Options = settings
	}

	return nil
}

func (o *Observation) panes(ctx context.Context, snapshot gotmux.Snapshot, outputLimit int64) error {
	var total int64

	for _, info := range snapshot.Panes() {
		index := slices.IndexFunc(o.Manifest.Panes, func(p model.Pane) bool { return p.ID == ref("p", string(info.ID)) })
		if index < 0 {
			continue
		}

		settings, err := options(ctx, "pane", info.Handle().Options())
		if err != nil {
			return err
		}

		o.Manifest.Panes[index].Options = settings

		data, omitted, err := captureWithinBudget(ctx, info.Handle(), min(outputLimit, model.MaxArtifactBytes, model.MaxGenerationBytes-total))
		if err != nil {
			return err
		}

		if omitted {
			continue
		}

		total += int64(len(data))
		hash := sha256.Sum256(data)
		id := ref("a", string(info.ID))
		o.Artifacts[id] = data
		o.Manifest.Artifacts = append(o.Manifest.Artifacts, model.Artifact{ID: id, Kind: "terminal-text", Length: int64(len(data)), SHA256: hex.EncodeToString(hash[:])})
		o.Manifest.Panes[index].Terminal.Artifact = id
	}

	return nil
}

func captureWithinBudget(ctx context.Context, pane gotmux.Pane, budget int64) ([]byte, bool, error) {
	if budget <= 0 {
		return nil, true, nil
	}

	var capture gotmux.CaptureOptions

	capture.EntireHistory, capture.IncludeEscapes, capture.PreserveSpaces = true, true, true
	capture.MaxBytes = budget

	data, err := pane.Capture(ctx, capture)
	if errors.Is(err, gotmux.ErrOutputLimit) {
		return nil, true, nil
	}

	if err != nil {
		return nil, false, oops.In("snapshot.capture").With("operation", "observe_terminal_text").Wrapf(err, "observe terminal text")
	}

	return data, false, nil
}

func compareGraph(ctx context.Context, server *gotmux.Server, before gotmux.Snapshot, selected []gotmux.SessionID, captured *model.Manifest, paneProcesses map[string]paneProcessRecord) error {
	after, err := reobserveGraph(ctx, server, before)
	if err != nil {
		return err
	}

	initial, _, err := tiledGraph(before, selected)
	if err != nil {
		return err
	}

	baseline, err := stabilityBytes(initial)
	if err != nil {
		return err
	}

	projected, _, err := tiledGraph(after, selected)
	if err != nil {
		return err
	}

	data, err := stabilityBytes(projected)
	if err != nil {
		return err
	}

	if !bytes.Equal(baseline, data) {
		return ErrChanged
	}

	if err := compareSelectedProcesses(before, after, projected); err != nil {
		return err
	}

	if err := revalidateProcesses(ctx, captured.Panes, paneProcesses); err != nil {
		return err
	}

	for index := range captured.Panes {
		captured.Panes[index].Title = projected.Panes[index].Title
	}

	return nil
}

func probeInitialSnapshot(ctx context.Context, server *gotmux.Server) (gotmux.ServerInfo, gotmux.Snapshot, error) {
	info, err := server.Probe(ctx)
	if err != nil {
		return gotmux.ServerInfo{}, gotmux.Snapshot{}, oops.In("snapshot.capture").With("operation", "probe_capture_source").Wrapf(err, "probe capture source")
	}

	before, err := server.Snapshot(ctx)
	if err != nil {
		return gotmux.ServerInfo{}, gotmux.Snapshot{}, oops.In("snapshot.capture").With("operation", "observe_source_graph").Wrapf(err, "observe source graph")
	}

	if !info.Identity.Equal(before.Identity) {
		return gotmux.ServerInfo{}, gotmux.Snapshot{}, ErrChanged
	}

	return info, before, nil
}

func revalidateProcesses(ctx context.Context, panes []model.Pane, paneProcesses map[string]paneProcessRecord) error {
	if paneProcesses == nil {
		return nil
	}

	for _, pane := range panes {
		expected, exists := paneProcesses[pane.ID]
		if !exists {
			return ErrChanged
		}

		if expected.dead {
			continue
		}

		postBirth, err := process.ReadIdentity(ctx, expected.pid)
		if err != nil {
			return errors.Join(ErrChanged, oops.In("snapshot.capture").
				With("operation", "revalidate_process").
				Wrapf(err, "revalidate process birth"))
		}

		if postBirth != expected.birth {
			return errors.Join(ErrChanged, oops.In("snapshot.capture").
				Code("process_identity_mismatch").
				With("operation", "revalidate_process").
				Wrapf(process.ErrIdentityMismatch, "process birth mismatch"))
		}

		if expected.foreground != nil {
			foreground, err := process.ObserveForeground(ctx, process.ForegroundRequest{
				PanePID:           expected.pid,
				ExpectedPaneBirth: expected.birth,
				EnvironmentKeys:   nil,
			})
			if err != nil {
				return errors.Join(ErrChanged, err)
			}

			if !sameProcessObservation(foreground, expected.foreground) {
				return ErrChanged
			}
		}
	}

	return nil
}

func reobserveGraph(ctx context.Context, server *gotmux.Server, before gotmux.Snapshot) (gotmux.Snapshot, error) {
	after, err := server.Snapshot(ctx)
	if err != nil {
		return gotmux.Snapshot{}, oops.In("snapshot.capture").With("operation", "reobserve_source_graph").Wrapf(err, "reobserve source graph")
	}

	if !before.Identity.Equal(after.Identity) {
		return gotmux.Snapshot{}, ErrChanged
	}

	afterProbe, err := server.Probe(ctx)
	if err != nil {
		return gotmux.Snapshot{}, oops.In("snapshot.capture").With("operation", "reprobe_capture_source").Wrapf(err, "reprobe capture source")
	}

	if !afterProbe.Identity.Equal(after.Identity) {
		return gotmux.Snapshot{}, ErrChanged
	}

	return after, nil
}

func stabilityBytes(m model.Manifest) ([]byte, error) {
	m.Panes = slices.Clone(m.Panes)
	for index := range m.Panes {
		m.Panes[index].Title = nil
		m.Panes[index].Terminal = model.Terminal{Artifact: "", History: 0, Alternate: false, CursorX: 0, CursorY: 0, Mode: nil}
	}

	data, err := json.Marshal(m)
	if err != nil {
		return nil, oops.In("snapshot.capture").With("operation", "compare_graph").Wrapf(err, "encode graph observation")
	}

	return data, nil
}

func compareSelectedProcesses(before, after gotmux.Snapshot, projected model.Manifest) error {
	wanted := make(map[string]bool, len(projected.Panes))
	for _, pane := range projected.Panes {
		wanted[pane.ID] = true
	}

	for _, pane := range before.Panes() {
		if !wanted[ref("p", string(pane.ID))] {
			continue
		}

		next, found := after.Pane(pane.ID)
		if !found || next.PID != pane.PID || next.Dead != pane.Dead {
			return ErrChanged
		}
	}

	return nil
}

func observationEvidence(enriched, floatingOmitted bool) []model.Evidence {
	domains := []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"}

	result := make([]model.Evidence, 0, len(domains))
	for _, domain := range domains {
		evidence := model.Evidence{Domain: domain, Status: "observed", Reason: "observed_only"}
		switch domain {
		case "S03":
			if floatingOmitted {
				evidence.Reason = string(model.ReasonFloatingPanesOmitted)
			}
		case "S06":
			evidence.Reason = "terminal_checkpoint_unavailable"
		case "S07":
			if !enriched {
				evidence.Status, evidence.Reason = "unverified", "not_captured"
			}
		case "S08":
			evidence.Status, evidence.Reason = "unverified", "application_checkpoint_unavailable"
		}

		result = append(result, evidence)
	}

	return result
}
