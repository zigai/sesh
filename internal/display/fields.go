package display

import "slices"

const (
	ProviderSession     FieldProvider = "session"
	ProviderAgent       FieldProvider = "agent"
	ProviderClock       FieldProvider = "clock"
	ProviderGit         FieldProvider = "git"
	ProviderAHT         FieldProvider = "aht"
	ProviderResource    FieldProvider = "resource"
	ProviderGH          FieldProvider = "gh"
	markerCellWidth                   = 2
	compactAgeCellWidth               = 3
)

var fieldRegistry = buildFieldRegistry()

type FieldProvider string

type FieldMetadata struct {
	Provider     FieldProvider
	WidthBound   int
	TruncateLeft bool
	Flexible     bool
	SessionRows  bool
	AgentRows    bool
	Compact      string
}

func FieldInfo(field string) (FieldMetadata, bool) {
	info, ok := fieldRegistry[field]
	return info, ok
}

func FieldsByProvider(provider FieldProvider) []string {
	fields := make([]string, 0)

	for field, info := range fieldRegistry {
		if info.Provider == provider {
			fields = append(fields, field)
		}
	}

	slices.Sort(fields)

	return fields
}

func buildFieldRegistry() map[string]FieldMetadata {
	registry := make(map[string]FieldMetadata)

	sessionFields := []string{
		FieldSessionName, FieldSessionID, FieldSessionKind, FieldSource, FieldConnection,
		FieldSourcePrefix, FieldSourceStatus, FieldWindowCount, FieldActivityAge,
		FieldAttached, FieldCurrent, FieldCurrentMarker, FieldAttachedMarker,
		FieldActiveWindowName, FieldActiveWindowIndex, FieldActivePanePath,
		FieldActivePanePathName, FieldActivePaneCommand, FieldActivePaneTitle,
		FieldAgentCount, FieldAgentTotal, FieldAgentWorking, FieldAgentWaiting,
		FieldAgentIdle, FieldAgentFailed, FieldAgentInterrupted, FieldAgentUnknown,
		FieldAgentStatus, FieldAgentStatusGlyph, FieldAgentStatusText, FieldAgentAge,
		FieldAgentDuration, FieldAgentClock, FieldSessionAgentContext, FieldShortcut,
		FieldShortcutBadge,
	}
	for _, field := range sessionFields {
		registry[field] = FieldMetadata{
			Provider: ProviderSession, WidthBound: 0, TruncateLeft: false,
			Flexible: false, SessionRows: true, AgentRows: true, Compact: "",
		}
	}

	agentFields := []string{
		FieldTree, FieldTreeBranch, FieldTreeIndent, FieldHarness, FieldHarnessRaw,
		FieldAgentSession, FieldRegistryID, FieldPaneID, FieldStatus, FieldStatusGlyph,
		FieldStatusText, FieldStatusAge, FieldStatusDuration, FieldStatusClock,
		FieldActiveMarker, FieldStale, FieldAgentContext, FieldAgentCWD,
		FieldAgentCWDName, FieldAgentProjectRoot, FieldAgentProjectName,
		FieldAgentGitBranch, FieldAgentGitGlyph, FieldAgentGitChanges,
		FieldAgentGitInfo, FieldAgentGitLabel, FieldAgentGitAdded,
		FieldAgentGitDeleted, FieldAgentGitAhead, FieldAgentGitBehind,
		FieldAgentWorktree, FieldAgentWorktreeName, FieldAgentSessionPath,
		FieldAgentSessionFile, FieldAgentExecutable, FieldAgentPID,
		FieldAgentModel, FieldAgentWaitingReason, FieldAgentFailureReason, FieldAgentLastTool, FieldAgentTask,
		FieldAgentContextPct, FieldAgentTokens, FieldAgentCost, FieldAgentTodoDone,
		FieldAgentTodoTotal, FieldAgentSubagents, FieldSubagentCount, FieldAgentPromptAge,
		FieldAgentPR, FieldAgentCI, FieldAgentGitStash, FieldAgentGitConflicts,
		FieldAgentGitDirty, FieldPaneCPU, FieldPaneMem, FieldWindowCPU, FieldWindowMem,
		FieldSessionCPU, FieldSessionMem,
	}
	for _, field := range agentFields {
		registry[field] = FieldMetadata{
			Provider: ProviderAgent, WidthBound: 0, TruncateLeft: false,
			Flexible: false, SessionRows: false, AgentRows: true, Compact: "",
		}
	}

	setFieldProvider(registry, ProviderClock, []string{
		FieldActivityAge, FieldAgentAge, FieldAgentDuration, FieldAgentClock,
		FieldStatusAge, FieldStatusDuration, FieldStatusClock,
	})

	setFieldProvider(registry, ProviderGit, []string{
		FieldAgentGitBranch, FieldAgentGitGlyph, FieldAgentGitChanges,
		FieldAgentGitInfo, FieldAgentGitLabel, FieldAgentGitAdded,
		FieldAgentGitDeleted, FieldAgentGitAhead, FieldAgentGitBehind,
		FieldAgentWorktree, FieldAgentWorktreeName, FieldAgentGitStash,
		FieldAgentGitConflicts, FieldAgentGitDirty,
	})

	setFieldProvider(registry, ProviderAHT, []string{
		FieldAgentModel, FieldAgentWaitingReason, FieldAgentFailureReason, FieldAgentLastTool,
		FieldAgentTask, FieldAgentContextPct, FieldAgentTokens, FieldAgentCost,
		FieldAgentTodoDone, FieldAgentTodoTotal, FieldAgentSubagents,
		FieldSubagentCount, FieldAgentPromptAge,
	})

	setFieldProvider(registry, ProviderResource, []string{
		FieldPaneCPU, FieldPaneMem, FieldWindowCPU,
		FieldWindowMem, FieldSessionCPU, FieldSessionMem,
	})

	for _, field := range []string{FieldWindowCPU, FieldWindowMem, FieldSessionCPU, FieldSessionMem} {
		info := registry[field]
		info.SessionRows = true
		registry[field] = info
	}

	setFieldProvider(registry, ProviderGH, []string{FieldAgentPR, FieldAgentCI})

	for field, bound := range map[string]int{
		FieldCurrentMarker: markerCellWidth, FieldAttachedMarker: markerCellWidth, FieldAgentGitGlyph: 1,
		FieldStatusGlyph: 1, FieldAgentStatusGlyph: 1, FieldActiveMarker: markerCellWidth,
		FieldAgentPromptAge: compactAgeCellWidth,
	} {
		info := registry[field]
		info.WidthBound = bound
		registry[field] = info
	}

	for _, field := range []string{FieldSessionName, FieldAgentSession} {
		info := registry[field]
		info.Flexible = true
		info.TruncateLeft = field == FieldAgentSession
		registry[field] = info
	}

	for _, field := range []string{
		FieldAgentCWD, FieldAgentCWDName, FieldAgentProjectRoot,
		FieldAgentProjectName, FieldAgentSessionPath, FieldAgentSessionFile,
	} {
		info := registry[field]
		info.TruncateLeft = true
		registry[field] = info
	}

	for field, compact := range map[string]string{
		FieldStatus:           FieldStatusGlyph,
		FieldStatusText:       FieldStatusGlyph,
		FieldAgentCWD:         FieldAgentCWDName,
		FieldAgentProjectRoot: FieldAgentProjectName,
		FieldAgentSessionPath: FieldAgentSessionFile,
		FieldAgentWorktree:    FieldAgentWorktreeName,
		FieldAgentGitInfo:     FieldAgentGitBranch,
		FieldAgentGitLabel:    FieldAgentGitBranch,
	} {
		info := registry[field]
		info.Compact = compact
		registry[field] = info
	}

	return registry
}

func setFieldProvider(registry map[string]FieldMetadata, provider FieldProvider, fields []string) {
	for _, field := range fields {
		info := registry[field]
		info.Provider = provider
		registry[field] = info
	}
}
