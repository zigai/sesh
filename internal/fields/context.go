package fields

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	SchemaVersion = 1

	RowSession Row = "session"
	RowAgent   Row = "agent"

	originLocal = "local"
)

type Row string

type Context struct {
	SchemaVersion int     `json:"schemaVersion"`
	Field         string  `json:"field"`
	Row           Row     `json:"row"`
	Session       Session `json:"session"`
	Agent         *Agent  `json:"agent,omitempty"`
}

type Session struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Kind              string    `json:"kind"`
	Path              string    `json:"path"`
	Origin            string    `json:"origin"`
	ConnectionID      string    `json:"connectionId"`
	WindowCount       int       `json:"windowCount"`
	Attached          bool      `json:"attached"`
	Activity          time.Time `json:"activity,omitzero"`
	ActivePaneID      string    `json:"activePaneId"`
	ActivePanePath    string    `json:"activePanePath"`
	ActivePaneCommand string    `json:"activePaneCommand"`
	ActivePaneTitle   string    `json:"activePaneTitle"`
}

type Agent struct {
	PaneID      string    `json:"paneId"`
	Harness     string    `json:"harness"`
	Status      string    `json:"status"`
	StatusSince time.Time `json:"statusSince,omitzero"`
	CWD         string    `json:"cwd"`
	ProjectRoot string    `json:"projectRoot"`
	GitBranch   string    `json:"gitBranch"`
	Worktree    string    `json:"worktree"`
	SessionPath string    `json:"sessionPath"`
	PID         int       `json:"pid"`
	Model       string    `json:"model"`
	Task        string    `json:"task"`
}

func (c Context) Encode() ([]byte, error) {
	c.SchemaVersion = SchemaVersion

	data, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode field context: %w", err)
	}

	return append(data, '\n'), nil
}

func (c Context) ChangeKey() (string, error) {
	c.Field = ""
	c.Session.Activity = time.Time{}

	if c.Agent != nil {
		agent := *c.Agent
		agent.StatusSince = time.Time{}
		c.Agent = &agent
	}

	data, err := c.Encode()
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

func (c Context) Env() []string {
	var agent Agent
	if c.Agent != nil {
		agent = *c.Agent
	}

	paneID := c.Session.ActivePaneID
	if c.Row == RowAgent {
		paneID = agent.PaneID
	}

	pid := ""
	if agent.PID > 0 {
		pid = strconv.Itoa(agent.PID)
	}

	return []string{
		"SESH_FIELD=" + c.Field,
		"SESH_ROW=" + string(c.Row),
		"SESH_SESSION=" + c.Session.Name,
		"SESH_SESSION_ID=" + c.Session.ID,
		"SESH_SESSION_KIND=" + c.Session.Kind,
		"SESH_SESSION_PATH=" + c.Session.Path,
		"SESH_ORIGIN=" + c.Session.Origin,
		"SESH_CONNECTION_ID=" + c.Session.ConnectionID,
		"SESH_PANE_ID=" + paneID,
		"SESH_AGENT_HARNESS=" + agent.Harness,
		"SESH_AGENT_STATUS=" + agent.Status,
		"SESH_AGENT_CWD=" + agent.CWD,
		"SESH_AGENT_PROJECT_ROOT=" + agent.ProjectRoot,
		"SESH_AGENT_GIT_BRANCH=" + agent.GitBranch,
		"SESH_AGENT_PID=" + pid,
	}
}

func (c Context) WorkingDir() string {
	if c.Session.Origin != originLocal {
		return ""
	}

	dir := c.Session.Path
	if c.Row == RowAgent && c.Agent != nil {
		dir = c.Agent.CWD
	}

	if dir == "" {
		return ""
	}

	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}

	return dir
}

func mergeEnv(base, overrides []string) []string {
	keys := make(map[string]struct{}, len(overrides))
	for _, entry := range overrides {
		key, _, _ := strings.Cut(entry, "=")
		keys[key] = struct{}{}
	}

	env := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := keys[key]; !ok {
			env = append(env, entry)
		}
	}

	return append(env, overrides...)
}
