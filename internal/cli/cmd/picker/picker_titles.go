//go:build linux || darwin

package cmdpicker

import (
	"context"
	"strings"

	"github.com/zigai/aht/pkg/aht"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/session"
)

func annotatePickerNativeTitles(ctx context.Context, observed []agents.AgentSession, summaries []session.AgentSummary) {
	lookups := make([]aht.Session, 0, len(observed))
	registryIDs := make([]string, 0, len(observed))

	for _, agent := range observed {
		lookup, ok := pickerTitleLookupSession(agent)
		if !ok {
			continue
		}

		lookups = append(lookups, lookup)
		registryIDs = append(registryIDs, agent.ID)
	}

	if len(lookups) == 0 {
		return
	}

	titles, _ := aht.LookupTitles(ctx, lookups)
	byRegistryID := make(map[string]string, len(titles))

	for i, title := range titles {
		if title = strings.TrimSpace(title); title != "" {
			byRegistryID[registryIDs[i]] = title
		}
	}

	for i := range summaries {
		for j := range summaries[i].Agents {
			agent := &summaries[i].Agents[j]
			if title := byRegistryID[agent.RegistryID]; title != "" {
				agent.HarnessSession = title
			}
		}
	}
}

func pickerTitleLookupSession(agent agents.AgentSession) (aht.Session, bool) {
	var lookup aht.Session

	if agent.ID == "" || agent.HarnessSessionID == "" || agent.Harness == string(aht.HarnessPi) {
		return lookup, false
	}

	capabilities, ok := aht.Capabilities(aht.Harness(agent.Harness))
	if !ok || !capabilities.TitleLookup {
		return lookup, false
	}

	lookup.Harness = aht.Harness(agent.Harness)
	lookup.SessionID = agent.HarnessSessionID
	lookup.SessionPath = agent.SessionPath

	return lookup, true
}
