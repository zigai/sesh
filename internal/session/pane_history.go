//go:build linux || darwin

package session

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"
)

const (
	maxPaneHistoryInventory = 16384
	defaultPanesPerSession  = 4
)

var ErrPaneHistoryLimitExceeded = errors.New("pane history inventory limit exceeded")

type PaneHistory struct {
	SessionName string
	SessionID   string
	WindowIndex int
	PaneIndex   int
	Lines       int
	NameRaw     RawBytes
}

type HistorySummary struct {
	SessionName string
	NameRaw     RawBytes
	PaneCount   int
	Lines       int
}

func ServerAbsent(err error) bool {
	return serverAbsent(err)
}

func ListPaneHistory(ctx context.Context, server *gotmux.Server) ([]PaneHistory, error) {
	if server == nil {
		return nil, nil
	}

	probeInfo, absent, err := probeServerProbe(ctx, server)
	if err != nil || absent {
		return []PaneHistory{}, err
	}

	sessionInfos, absent, err := listSessionInfos(ctx, server)
	if err != nil || absent {
		return []PaneHistory{}, err
	}

	panes, limitHit, err := collectAllPanes(ctx, sessionInfos)
	if err != nil {
		return nil, err
	}

	if err := verifyPostProbe(ctx, server, probeInfo); err != nil {
		return nil, err
	}

	if limitHit {
		return panes, ErrPaneHistoryLimitExceeded
	}

	return panes, nil
}

func SortPaneHistory(panes []PaneHistory) {
	slices.SortFunc(panes, func(a, b PaneHistory) int {
		if c := cmp.Compare(a.SessionName, b.SessionName); c != 0 {
			return c
		}

		if c := cmp.Compare(a.WindowIndex, b.WindowIndex); c != 0 {
			return c
		}

		if c := cmp.Compare(a.PaneIndex, b.PaneIndex); c != 0 {
			return c
		}

		return cmp.Compare(string(a.NameRaw), string(b.NameRaw))
	})
}

func SummarizePaneHistory(panes []PaneHistory) []HistorySummary {
	bySession := make(map[string]HistorySummary)
	for _, pane := range panes {
		summary := bySession[pane.SessionName]
		summary.SessionName = pane.SessionName

		if len(summary.NameRaw) == 0 && len(pane.NameRaw) > 0 {
			summary.NameRaw = pane.NameRaw
		}

		summary.PaneCount++
		summary.Lines += pane.Lines
		bySession[pane.SessionName] = summary
	}

	summaries := make([]HistorySummary, 0, len(bySession))
	for _, summary := range bySession {
		summaries = append(summaries, summary)
	}

	slices.SortFunc(summaries, func(a, b HistorySummary) int {
		return cmp.Or(
			cmp.Compare(b.Lines, a.Lines),
			cmp.Compare(a.SessionName, b.SessionName),
			cmp.Compare(string(a.NameRaw), string(b.NameRaw)),
		)
	})

	return summaries
}

func probeServerProbe(ctx context.Context, server *gotmux.Server) (gotmux.ServerInfo, bool, error) {
	var zeroInfo gotmux.ServerInfo

	probeInfo, err := server.Probe(ctx)
	if err != nil {
		if serverAbsent(err) {
			return zeroInfo, true, nil
		}

		return zeroInfo, false, oops.In("session").Code("history_failed").With("operation", "list_pane_history").Wrapf(err, "probe server")
	}

	return probeInfo, false, nil
}

func listSessionInfos(ctx context.Context, server *gotmux.Server) ([]gotmux.SessionInfo, bool, error) {
	sessionInfos, err := server.Sessions(ctx)
	if err != nil {
		if serverAbsent(err) {
			return nil, true, nil
		}

		return nil, false, oops.In("session").Code("history_failed").With("operation", "list_pane_history").Wrapf(err, "listing sessions")
	}

	return sessionInfos, false, nil
}

func collectAllPanes(ctx context.Context, sessionInfos []gotmux.SessionInfo) ([]PaneHistory, bool, error) {
	limitHit := false
	panes := make([]PaneHistory, 0, min(len(sessionInfos)*defaultPanesPerSession, maxPaneHistoryInventory))

	for _, sessInfo := range sessionInfos {
		if ctx.Err() != nil {
			return nil, false, oops.In("session").Code("history_failed").With("operation", "list_pane_history").Wrap(ctx.Err())
		}

		sessPanes, err := querySessionPanes(ctx, sessInfo)
		if err != nil {
			return nil, false, err
		}

		panes, limitHit = appendSessionPanes(panes, sessPanes, sessInfo)
		if limitHit {
			break
		}
	}

	return panes, limitHit, nil
}

func verifyPostProbe(ctx context.Context, server *gotmux.Server, probeInfo gotmux.ServerInfo) error {
	postProbe, err := server.Probe(ctx)
	if err != nil {
		if serverAbsent(err) {
			return nil
		}

		return oops.In("session").Code("history_failed").With("operation", "list_pane_history").Wrapf(err, "post-probe server")
	}

	if !postProbe.Identity.Equal(probeInfo.Identity) {
		return oops.In("session").Code("history_failed").With("operation", "list_pane_history").Wrap(ErrServerIdentityChanged)
	}

	return nil
}

func querySessionPanes(ctx context.Context, sessInfo gotmux.SessionInfo) ([]gotmux.PaneInfo, error) {
	panes, err := sessInfo.Handle().Panes(ctx)
	if err != nil {
		if serverAbsent(err) || errors.Is(err, gotmux.ErrNotFound) {
			return nil, nil
		}

		return nil, oops.In("session").Code("history_failed").With("operation", "list_pane_history").Wrapf(err, "listing session panes")
	}

	return panes, nil
}

func appendSessionPanes(panes []PaneHistory, sessPanes []gotmux.PaneInfo, sessInfo gotmux.SessionInfo) ([]PaneHistory, bool) {
	rawName, hasRaw := sessInfo.Raw("session_name")

	var nameRaw RawBytes
	if hasRaw && len(rawName) > 0 {
		nameRaw = slices.Clone(rawName)
	} else {
		nameRaw = slices.Clone([]byte(sessInfo.Name))
	}

	for _, p := range sessPanes {
		if len(panes) >= maxPaneHistoryInventory {
			return panes, true
		}

		wIndex := 0
		if idx, ok := p.WindowIndex.Get(); ok {
			wIndex = idx
		}

		panes = append(panes, PaneHistory{
			SessionName: sessInfo.Name,
			SessionID:   string(sessInfo.ID),
			WindowIndex: wIndex,
			PaneIndex:   p.Index,
			Lines:       p.HistorySize,
			NameRaw:     nameRaw,
		})
	}

	return panes, false
}
