package restore

import (
	"bytes"
	"errors"
	"slices"
	"strings"

	"github.com/zigai/sesh/internal/snapshot/model"
)

type MissingRequirement struct {
	Domain string `json:"domain"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type RequirementClassification struct {
	FidelityLimits []MissingRequirement
	Blockers       []MissingRequirement
}

type PlanSummary struct {
	SessionCount      int `json:"sessionCount"`
	GroupCount        int `json:"groupCount"`
	WindowCount       int `json:"windowCount"`
	PaneCount         int `json:"paneCount"`
	LinkCount         int `json:"linkCount"`
	StepCount         int `json:"stepCount"`
	SharedWindowCount int `json:"sharedWindowCount"`
	SharedPaneCount   int `json:"sharedPaneCount"`
}

type Plan struct {
	RequestedSessions []string `json:"requestedSessions"`

	AddedSessions []string `json:"addedSessions"`

	ClosureSessions []string `json:"closureSessions"`

	SourceGraph model.Manifest `json:"sourceGraph"`

	Steps []Step `json:"steps"`

	MissingRequirements []MissingRequirement `json:"missingRequirements"`

	Summary PlanSummary `json:"summary"`
}

func (p Plan) SourceEvidenceComplete() bool {
	return p.SourceGraph.Complete()
}

func (p Plan) PracticalRecoveryReady() bool {
	classification := ClassifyPracticalRequirements(p.MissingRequirements)

	return p.SourceGraph.PracticalRecoveryReady() && len(classification.Blockers) == 0
}

func ClassifyPracticalRequirements(requirements []MissingRequirement) RequirementClassification {
	classification := RequirementClassification{
		FidelityLimits: make([]MissingRequirement, 0, len(requirements)),
		Blockers:       make([]MissingRequirement, 0),
	}

	for _, requirement := range requirements {
		if practicalFidelityReason(requirement.Reason) {
			classification.FidelityLimits = append(classification.FidelityLimits, requirement)
		} else {
			classification.Blockers = append(classification.Blockers, requirement)
		}
	}

	return classification
}

func BuildPlan(m model.Manifest, selected []string) (Plan, error) {
	if err := m.Validate(); err != nil {
		if errors.Is(err, model.ErrVersion) {
			return Plan{}, versionFailure("manifest", err)
		}

		return Plan{}, planFailure("manifest", "invalid_manifest", err)
	}

	requested, err := validateSelection(m, selected)
	if err != nil {
		return Plan{}, err
	}

	sourceGraph, closureIDs, added, err := deriveSourceGraph(m, requested)
	if err != nil {
		return Plan{}, err
	}

	if err := validateNames(sourceGraph); err != nil {
		return Plan{}, err
	}

	if err := validatePlanningBudget(sourceGraph); err != nil {
		return Plan{}, err
	}

	missing := collectMissingRequirements(sourceGraph)
	steps, summary := generateSteps(sourceGraph)

	plan := Plan{
		RequestedSessions:   slices.Clone(requested),
		AddedSessions:       slices.Clone(added),
		ClosureSessions:     slices.Clone(closureIDs),
		SourceGraph:         sourceGraph,
		Steps:               steps,
		MissingRequirements: missing,
		Summary:             summary,
	}

	if err := ValidatePlan(plan); err != nil {
		return Plan{}, err
	}

	return plan, nil
}

func validateNames(m model.Manifest) error {
	if err := validateSessionNames(m.Sessions); err != nil {
		return err
	}

	if err := validateGroupNames(m.Groups); err != nil {
		return err
	}

	if err := validateWindowNames(m.Windows); err != nil {
		return err
	}

	return validatePaneTitles(m.Panes)
}

func validateSessionNames(sessions []model.Session) error {
	for _, s := range sessions {
		if bytes.ContainsRune(s.Name, 0) || bytes.ContainsAny(s.Name, ":.\\") {
			return planFailure("manifest", "target_invalid_session_name", ErrInvalid)
		}

		for _, b := range s.Name {
			if b < 32 || b == 127 {
				return planFailure("manifest", "target_invalid_session_name", ErrInvalid)
			}
		}
	}

	return nil
}

func validateGroupNames(groups []model.Group) error {
	for _, g := range groups {
		if bytes.ContainsRune(g.Name, 0) || bytes.ContainsAny(g.Name, ":.\\") {
			return planFailure("manifest", "target_invalid_group_name", ErrInvalid)
		}

		for _, b := range g.Name {
			if b < 32 || b == 127 {
				return planFailure("manifest", "target_invalid_group_name", ErrInvalid)
			}
		}
	}

	return nil
}

func validateWindowNames(windows []model.Window) error {
	for _, w := range windows {
		if bytes.ContainsRune(w.Name, 0) {
			return planFailure("manifest", "target_invalid_window_name", ErrInvalid)
		}
	}

	return nil
}

func validatePaneTitles(panes []model.Pane) error {
	for _, p := range panes {
		if bytes.ContainsRune(p.Title, 0) {
			return planFailure("manifest", "target_invalid_pane_title", ErrInvalid)
		}
	}

	return nil
}

func validateSelection(m model.Manifest, selected []string) ([]string, error) {
	if len(selected) == 0 {
		return nil, planFailure("selection", "empty_selection", ErrInvalid)
	}

	if len(selected) > model.MaxEntities {
		return nil, planFailure("selection", "bounds", ErrInvalid)
	}

	known := make(map[string]bool, len(m.Sessions))
	for _, session := range m.Sessions {
		known[session.ID] = true
	}

	requestedSet := make(map[string]bool, len(selected))
	for _, id := range selected {
		if !model.ValidID(id) {
			return nil, planFailure("selection", "malformed_session", ErrInvalid)
		}

		if !known[id] {
			return nil, planFailure("selection", "unknown_session", ErrInvalid)
		}

		requestedSet[id] = true
	}

	requested := make([]string, 0, len(requestedSet))
	for id := range requestedSet {
		requested = append(requested, id)
	}

	slices.Sort(requested)

	return requested, nil
}

func deriveSourceGraph(m model.Manifest, requested []string) (model.Manifest, []string, []string, error) {
	closureIDs, err := m.Closure(requested)
	if err != nil {
		return model.Manifest{}, nil, nil, planFailure("closure", "closure_failed", err)
	}

	slices.Sort(closureIDs)

	requestedSet := make(map[string]bool, len(requested))
	for _, id := range requested {
		requestedSet[id] = true
	}

	added := make([]string, 0, len(closureIDs)-len(requested))
	for _, id := range closureIDs {
		if !requestedSet[id] {
			added = append(added, id)
		}
	}

	slices.Sort(added)

	if len(closureIDs) == len(m.Sessions) {
		removal, err := model.RemoveSessions(m, nil)
		if err != nil {
			return model.Manifest{}, nil, nil, planFailure("source_graph", "clone_failed", err)
		}

		return removal.Retained, closureIDs, added, nil
	}

	closureSet := make(map[string]bool, len(closureIDs))
	for _, id := range closureIDs {
		closureSet[id] = true
	}

	var unselected []string

	for _, s := range m.Sessions {
		if !closureSet[s.ID] {
			unselected = append(unselected, s.ID)
		}
	}

	removal, err := model.RemoveSessions(m, unselected)
	if err != nil {
		return model.Manifest{}, nil, nil, planFailure("source_graph", "prune_failed", err)
	}

	return removal.Retained, closureIDs, added, nil
}

func collectMissingRequirements(m model.Manifest) []MissingRequirement {
	missing := []MissingRequirement{
		{
			Domain: "atomicity",
			Status: "unsupported",
			Reason: "atomic_restore_transaction_unavailable",
		},
		{
			Domain: "target",
			Status: "unsupported",
			Reason: "target_options_and_group_names_require_adaptation",
		},
		{
			Domain: "launch",
			Status: "unsupported",
			Reason: "exactly_once_process_launch_unavailable",
		},
		{
			Domain: "ownership",
			Status: "unsupported",
			Reason: "immutable_ownership_enforcement_unavailable",
		},
		{
			Domain: "terminal",
			Status: "unsupported",
			Reason: "terminal_state_checkpoint_unavailable",
		},
	}

	if m.Source.Consistency != "checkpoint" {
		missing = append(missing, MissingRequirement{
			Domain: "S00_Consistency",
			Status: m.Source.Consistency,
			Reason: "capture_not_atomic",
		})
	}

	for _, e := range m.Evidence {
		if e.Status != "verified" || e.Reason != "" {
			missing = append(missing, MissingRequirement{
				Domain: e.Domain,
				Status: e.Status,
				Reason: e.Reason,
			})
		}
	}

	for _, p := range m.Panes {
		if p.Launch.Recovery == model.RecoveryNonrestartable && p.Launch.Reason != "" {
			missing = append(missing, MissingRequirement{
				Domain: "launch",
				Status: "unsupported",
				Reason: p.Launch.Reason,
			})
		}
	}

	slices.SortFunc(missing, func(a, b MissingRequirement) int {
		return strings.Compare(a.Domain, b.Domain)
	})

	return missing
}

func practicalFidelityReason(reason string) bool {
	return slices.Contains([]string{
		"application_checkpoint_unavailable",
		"application_restart_unsupported",
		"approved_launch_unavailable",
		"atomic_restore_transaction_unavailable",
		"capture_not_atomic",
		"dead_pane",
		"directory_deleted",
		"executable_deleted",
		"exactly_once_process_launch_unavailable",
		"global_scope_environment_materialized_locally",
		"global_scope_options_not_applied",
		"immutable_ownership_enforcement_unavailable",
		"inherited_option_not_representable",
		"inherited_options_materialized_locally",
		"launch_context_nonrestartable",
		"launch_environment_incomplete",
		"nonrestartable_pane",
		"not_captured",
		"observed_only",
		"ownership_unverified",
		"pane_dead",
		"pane_launch_nonrestartable",
		"pane_not_restartable",
		"pane_process_deleted",
		"pane_process_terminated",
		"pane_process_unavailable",
		"process_cwd_deleted",
		"process_deleted",
		"process_executable_deleted",
		"process_identity_unavailable",
		"process_launch_not_attempted",
		"saved_working_directory_missing",
		"target_environment_masked_locally",
		"target_environment_unmasked",
		"target_inherited_options_unmasked",
		"target_options_and_group_names_require_adaptation",
		"terminal_checkpoint_unavailable",
		"terminal_state_checkpoint_unavailable",
		"working_directory_deleted",
	}, reason)
}
