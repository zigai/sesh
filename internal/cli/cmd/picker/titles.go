//go:build linux || darwin

package picker

import (
	"context"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/zigai/aht/v2/pkg/aht"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/zigai/sesh/internal/agents"
	"github.com/zigai/sesh/internal/session"
)

type titleCache struct {
	mu       sync.Mutex
	cache    map[string]titleState
	inflight singleflight.Group
}

type titleState struct {
	fileInfo os.FileInfo
	title    string
}

func (c *titleCache) annotate(ctx context.Context, observed []agents.AgentSession, summaries []session.AgentSummary) {
	lookups := make([]aht.Session, 0, len(observed))
	registryIDs := make([]string, 0, len(observed))

	for _, agent := range observed {
		lookup, ok := titleLookupSession(agent)
		if !ok {
			continue
		}

		lookups = append(lookups, lookup)
		registryIDs = append(registryIDs, agent.ID)
	}

	if len(lookups) == 0 {
		return
	}

	titles := make([]string, len(lookups))

	var readers errgroup.Group
	readers.SetLimit(runtime.GOMAXPROCS(0))

	for i := range lookups {
		readers.Go(func() error {
			titles[i] = c.title(ctx, lookups[i])

			return nil
		})
	}

	_ = readers.Wait()

	c.retain(lookups)

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
				agent.Task = title
			}
		}
	}
}

func (c *titleCache) title(ctx context.Context, lookup aht.Session) string {
	info, err := os.Lstat(lookup.SessionPath)
	if err != nil || !info.Mode().IsRegular() {
		found, _ := aht.SessionTitles(ctx, []aht.Session{lookup})

		return found[0]
	}

	key := titleCacheKey(lookup)

	c.mu.Lock()
	cached, ok := c.cache[key]
	c.mu.Unlock()

	if ok && titleSourceUnchanged(cached.fileInfo, info) {
		return cached.title
	}

	value, _, _ := c.inflight.Do(key, func() (any, error) {
		found, err := aht.SessionTitles(ctx, []aht.Session{lookup})
		if err == nil {
			c.mu.Lock()
			if c.cache == nil {
				c.cache = make(map[string]titleState)
			}

			c.cache[key] = titleState{fileInfo: info, title: found[0]}
			c.mu.Unlock()
		}

		return found[0], nil
	})

	title, _ := value.(string)

	return title
}

func (c *titleCache) retain(lookups []aht.Session) {
	current := make(map[string]bool, len(lookups))
	for _, lookup := range lookups {
		current[titleCacheKey(lookup)] = true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for key := range c.cache {
		if !current[key] {
			delete(c.cache, key)
		}
	}
}

func titleCacheKey(lookup aht.Session) string {
	return string(lookup.Harness) + "\x00" + lookup.SessionID + "\x00" + lookup.SessionPath
}

func titleSourceUnchanged(cached, current os.FileInfo) bool {
	return os.SameFile(cached, current) && cached.Size() == current.Size() && cached.ModTime().Equal(current.ModTime())
}

func titleLookupSession(agent agents.AgentSession) (aht.Session, bool) {
	var lookup aht.Session

	if agent.ID == "" || agent.HarnessSessionID == "" || agent.Harness == string(aht.HarnessPi) {
		return lookup, false
	}

	capabilities, ok := aht.Capabilities(aht.Harness(agent.Harness))
	if !ok || !capabilities.TitleLookup {
		return lookup, false
	}

	lookup.SchemaVersion = 3
	lookup.Harness = aht.Harness(agent.Harness)
	lookup.SessionID = agent.HarnessSessionID
	lookup.SessionPath = agent.SessionPath

	return lookup, true
}
