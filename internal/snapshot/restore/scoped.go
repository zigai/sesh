//go:build linux || darwin

package restore

import (
	"context"
	"maps"
	"slices"

	"github.com/samber/oops"
	gotmux "github.com/zigai/gotmux/tmux"

	"github.com/zigai/sesh/internal/integration/tmuxserver"
	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	cursorColorOption       = "cursor-colour"        //nolint:misspell // exact tmux option spelling
	promptCursorColorOption = "prompt-cursor-colour" //nolint:misspell // exact tmux option spelling
)

type optionScope interface {
	List(ctx context.Context) ([]gotmux.OptionEntry, error)
	ListWith(ctx context.Context, opts gotmux.ListOptionOptions) ([]gotmux.OptionEntry, error)
	Set(ctx context.Context, name, value string) error
	SetWith(ctx context.Context, name string, opts gotmux.SetOptionOptions) error
	Unset(ctx context.Context, name string) error
}

type optionShape struct {
	value   string
	present bool
}

type environmentKey struct {
	name   string
	hidden bool
}

type environmentShape struct {
	value  string
	unset  bool
	hidden bool
}

type ImportReport struct {
	Sessions    int                  `json:"sessions"`
	Windows     int                  `json:"windows"`
	Panes       int                  `json:"panes"`
	Options     int                  `json:"options"`
	Environment int                  `json:"environment"`
	Missing     []MissingRequirement `json:"missingRequirements"`
}

func ImportScopedState(ctx context.Context, server *gotmux.Server, plan Plan, receipts map[string]string) (ImportReport, error) {
	report := ImportReport{
		Sessions:    0,
		Windows:     0,
		Panes:       0,
		Options:     0,
		Environment: 0,
		Missing:     nil,
	}

	if server == nil {
		return report, oops.In("snapshot.restore").Code("endpoint_required").Wrap(tmuxserver.ErrEndpoint)
	}

	report.Missing = ScopedStateRequirements(plan)

	identities, err := ResolveRuntimeIdentities(ctx, server, plan, receipts)
	if err != nil {
		return report, err
	}

	if err := importSessionScopes(ctx, server, plan, identities, &report); err != nil {
		return report, err
	}

	if err := importWindowScopes(ctx, server, plan, identities, &report); err != nil {
		return report, err
	}

	if err := importPaneScopes(ctx, server, plan, identities, &report); err != nil {
		return report, err
	}

	return report, nil
}

func ObserveScopedStateConflicts(ctx context.Context, server *gotmux.Server, plan Plan, receipts map[string]string) ([]VerificationMismatch, error) {
	identities, err := ResolveRuntimeIdentities(ctx, server, plan, receipts)
	if err != nil {
		return nil, err
	}

	report := VerificationReport{
		Verified:      false,
		Sessions:      0,
		Windows:       0,
		Panes:         0,
		Links:         0,
		Mismatches:    nil,
		Advisories:    nil,
		VerifiedSteps: nil,
		RuntimePanes:  nil,
	}

	state := newVerifiedState()
	verifyScopedState(ctx, server, plan, identities, state, &report)

	return report.Mismatches, nil
}

func ScopedStateRequirements(plan Plan) []MissingRequirement {
	report := ImportReport{
		Sessions: 0, Windows: 0, Panes: 0, Options: 0, Environment: 0, Missing: nil,
	}

	for _, option := range plan.SourceGraph.Source.Options {
		if option.Scope == "server" || option.Scope == "global-session" || option.Scope == "global-window" {
			report.note("options", model.ReasonGlobalScopeOptionsNotApplied)
		}
	}

	if plan.UnsupportedOptions > 0 {
		report.note("options", model.ReasonTargetOptionUnsupported)
	}

	for _, variable := range plan.SourceGraph.Source.Environment {
		if variable.State == "set" || variable.State == "removed" || variable.State == "absent" {
			report.note("environment", model.ReasonGlobalScopeEnvironmentMaterializedLocally)

			break
		}
	}

	for _, session := range plan.SourceGraph.Sessions {
		report.noteInheritedOptions(session.Options)
	}

	for _, window := range plan.SourceGraph.Windows {
		report.noteInheritedOptions(window.Options)
	}

	return slices.Clone(report.Missing)
}

func (r *ImportReport) noteInheritedOptions(options []model.Option) {
	if hasUnsupportedInheritedOption(options) {
		r.note("options", model.ReasonInheritedOptionNotRepresentable)
	}

	if hasMaterializableInheritedOption(options) {
		r.note("options", model.ReasonInheritedOptionsMaterializedLocally)
	}
}

func importSessionScopes(ctx context.Context, server *gotmux.Server, plan Plan, identities RuntimeIdentities, report *ImportReport) error {
	targetGlobal, err := environmentTable(ctx, server.Environment())
	if err != nil {
		return err
	}

	for _, session := range plan.SourceGraph.Sessions {
		if err := importSessionScope(ctx, server, session, plan.SourceGraph.Source.Environment, identities, targetGlobal, report); err != nil {
			return err
		}
	}

	return nil
}

func importSessionScope(
	ctx context.Context,
	server *gotmux.Server,
	session model.Session,
	sourceGlobal []model.Variable,
	identities RuntimeIdentities,
	targetGlobal map[environmentKey]environmentShape,
	report *ImportReport,
) error {
	runtimeID, ok := identities.Sessions[session.ID]
	if !ok {
		return missingReceipt(session.ID)
	}

	handle, err := server.Session(ctx, runtimeID)
	if err != nil {
		return resolveFailure("session", err)
	}

	applied, err := applyOptions(ctx, session.Options, handle.Options(), true)
	if err != nil {
		return err
	}

	expectedEnvironment := effectiveEnvironmentVariables(sourceGlobal, session.Environment)

	variables, masked, err := applyEnvironment(ctx, expectedEnvironment, targetGlobal, handle.Environment())
	if err != nil {
		return err
	}

	if masked {
		report.note("environment", model.ReasonTargetEnvironmentMaskedLocally)
	}

	optionsMatch, err := effectiveOptionsMatch(ctx, handle.Options(), session.Options)
	if err != nil {
		return err
	}

	if !optionsMatch {
		report.note("options", model.ReasonTargetInheritedOptionsUnmasked)
	}

	environmentMatch, err := effectiveEnvironmentMatch(ctx, targetGlobal, handle.Environment(), sourceGlobal, session.Environment)
	if err != nil {
		return err
	}

	if !environmentMatch {
		report.note("environment", model.ReasonTargetEnvironmentUnmasked)
	}

	report.Sessions++
	report.Options += applied
	report.Environment += variables

	return nil
}

func importWindowScopes(ctx context.Context, server *gotmux.Server, plan Plan, identities RuntimeIdentities, report *ImportReport) error {
	for _, window := range plan.SourceGraph.Windows {
		runtimeID, ok := identities.Windows[window.ID]
		if !ok {
			return missingReceipt(window.ID)
		}

		handle, err := server.Window(ctx, runtimeID)
		if err != nil {
			return resolveFailure("window", err)
		}

		applied, err := applyOptions(ctx, window.Options, handle.Options(), true)
		if err != nil {
			return err
		}

		matches, err := effectiveOptionsMatch(ctx, handle.Options(), window.Options)
		if err != nil {
			return err
		}

		if !matches {
			report.note("options", model.ReasonTargetInheritedOptionsUnmasked)
		}

		report.Windows++
		report.Options += applied
	}

	return nil
}

func importPaneScopes(ctx context.Context, server *gotmux.Server, plan Plan, identities RuntimeIdentities, report *ImportReport) error {
	for _, pane := range plan.SourceGraph.Panes {
		runtimeID, ok := identities.Panes[pane.ID]
		if !ok {
			return missingReceipt(pane.ID)
		}

		handle, err := server.Pane(ctx, runtimeID)
		if err != nil {
			return resolveFailure("pane", err)
		}

		applied, err := applyOptions(ctx, pane.Options, handle.Options(), false)
		if err != nil {
			return err
		}

		matches, err := effectiveOptionsMatch(ctx, handle.Options(), pane.Options)
		if err != nil {
			return err
		}

		if !matches {
			report.note("options", model.ReasonTargetInheritedOptionsUnmasked)
		}

		report.Panes++
		report.Options += applied
	}

	return nil
}

func (r *ImportReport) note(domain string, reason model.Reason) {
	entry := MissingRequirement{Domain: domain, Status: "partial", Reason: reason}
	if slices.Contains(r.Missing, entry) {
		return
	}

	r.Missing = append(r.Missing, entry)
}

func resolveFailure(kind string, err error) error {
	return oops.In("snapshot.restore").
		Code("operation_uncertain").
		With("operation", "resolve_"+kind, "reason", "recorded_object_unavailable").
		Wrap(err)
}

func applyOptions(ctx context.Context, expected []model.Option, scope optionScope, materializeInherited bool) (int, error) {
	local, err := scope.ListWith(ctx, gotmux.ListOptionOptions{Inherited: false, Hooks: true})
	if err != nil {
		return 0, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "import_options", "reason", "scope_list_failed").
			Wrap(err)
	}

	wanted := wantedOptions(expected, materializeInherited)
	applied, err := applyWantedOptions(ctx, scope, wanted, optionEntries(local))

	return applied, err
}

func applyWantedOptions(ctx context.Context, scope optionScope, wanted, live map[string]optionShape) (int, error) {
	applied := 0

	for _, name := range slices.Sorted(maps.Keys(wanted)) {
		shape := wanted[name]
		if current, ok := live[name]; ok && current == shape {
			continue
		}

		// tmux lists a valueless local option only for an empty array, and setting an array to the
		// empty string is the command that recreates one.
		if err := scope.Set(ctx, name, shape.value); err != nil {
			return applied, oops.In("snapshot.restore").
				Code("operation_uncertain").
				With("operation", "import_options", "reason", "option_set_outcome_unknown").
				Wrap(err)
		}

		applied++
	}

	return applied, nil
}

func wantedOptions(expected []model.Option, materializeInherited bool) map[string]optionShape {
	wanted := make(map[string]optionShape, len(expected))
	for _, option := range expected {
		if len(option.Name) == 0 || option.Inherited && !materializeInherited {
			continue
		}

		if option.Inherited && !option.Present {
			continue
		}

		if inheritedOptionNotRepresentable(option) {
			continue
		}

		wanted[string(option.Name)] = optionShape{value: string(option.Value), present: option.Present}
	}

	return wanted
}

func hasUnsupportedInheritedOption(options []model.Option) bool {
	for _, option := range options {
		if option.Inherited && len(option.Name) > 0 &&
			(!option.Present || inheritedOptionNotRepresentable(option)) {
			return true
		}
	}

	return false
}

func hasMaterializableInheritedOption(options []model.Option) bool {
	for _, option := range options {
		if option.Inherited && option.Present && len(option.Name) > 0 && !inheritedOptionNotRepresentable(option) {
			return true
		}
	}

	return false
}

func inheritedOptionNotRepresentable(option model.Option) bool {
	if !option.Inherited || !option.Present || string(option.Value) != "none" {
		return false
	}

	name := string(option.Name)

	return name == cursorColorOption || name == promptCursorColorOption
}

func optionEntries(entries []gotmux.OptionEntry) map[string]optionShape {
	values := make(map[string]optionShape, len(entries))

	for _, entry := range entries {
		value, present := entry.Value.Get()
		values[entry.Name] = optionShape{value: value, present: present}
	}

	return values
}

func effectiveOptionsMatch(ctx context.Context, scope optionScope, expected []model.Option) (bool, error) {
	live, err := scope.ListWith(ctx, gotmux.ListOptionOptions{Inherited: true, Hooks: true})
	if err != nil {
		return false, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "inspect_options", "reason", "scope_list_failed").
			Wrap(err)
	}

	liveValues := optionEntries(live)

	wanted, ignored := effectiveOptions(expected)
	for name := range ignored {
		delete(liveValues, name)
	}

	return maps.Equal(liveValues, wanted), nil
}

func effectiveOptions(expected []model.Option) (map[string]optionShape, map[string]struct{}) {
	values := make(map[string]optionShape, len(expected))
	ignored := map[string]struct{}{}

	for _, option := range expected {
		if len(option.Name) == 0 {
			continue
		}

		if option.Inherited && !option.Present {
			ignored[string(option.Name)] = struct{}{}

			continue
		}

		values[string(option.Name)] = optionShape{value: string(option.Value), present: option.Present}
	}

	return values, ignored
}

func effectiveEnvironmentMatch(
	ctx context.Context,
	global map[environmentKey]environmentShape,
	scope gotmux.EnvironmentScope,
	expectedGlobal,
	expectedLocal []model.Variable,
) (bool, error) {
	liveLocal, err := environmentTable(ctx, scope)
	if err != nil {
		return false, err
	}

	wanted := effectiveEnvironment(expectedGlobal, expectedLocal)

	observed := effectiveEnvironment(environmentVariables(global), environmentVariables(liveLocal))
	for name, shape := range observed {
		if _, ok := wanted[name]; !ok && shape.unset {
			delete(observed, name)
		}
	}

	return maps.Equal(observed, wanted), nil
}

func effectiveEnvironmentVariables(global, local []model.Variable) []model.Variable {
	values := make(map[string]model.Variable, len(global)+len(local))
	for _, variable := range global {
		if variable.State == "absent" {
			continue
		}

		values[string(variable.Name)] = variable
	}

	for _, variable := range local {
		if variable.State == "absent" {
			continue
		}

		values[string(variable.Name)] = variable
	}

	result := make([]model.Variable, 0, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
		result = append(result, values[name])
	}

	return result
}

func effectiveEnvironment(global, local []model.Variable) map[string]environmentShape {
	values := map[string]environmentShape{}
	for _, variable := range effectiveEnvironmentVariables(global, local) {
		values[string(variable.Name)] = variableEnvironmentShape(variable)
	}

	return values
}

func variableEnvironmentShape(variable model.Variable) environmentShape {
	return environmentShape{
		value:  string(variable.Value),
		unset:  variable.State == "removed",
		hidden: variable.Hidden,
	}
}

func environmentVariables(table map[environmentKey]environmentShape) []model.Variable {
	variables := make([]model.Variable, 0, len(table))
	for key, shape := range table {
		state := "set"
		if shape.unset {
			state = "removed"
		}

		variables = append(variables, model.Variable{
			Name: model.Bytes(key.name), Value: model.Bytes(shape.value), State: state, Hidden: key.hidden,
		})
	}

	return variables
}

func environmentTable(ctx context.Context, scope gotmux.EnvironmentScope) (map[environmentKey]environmentShape, error) {
	observed := map[environmentKey]environmentShape{}
	seen := map[string]struct{}{}

	for _, hidden := range []bool{false, true} {
		entries, err := scope.ListWith(ctx, gotmux.ListEnvironmentOptions{Hidden: hidden})
		if err != nil {
			return nil, oops.In("snapshot.restore").
				Code("restore_failed").
				With("operation", "import_environment", "reason", "scope_list_failed").
				Wrap(err)
		}

		for _, entry := range entries {
			if _, duplicate := seen[entry.Name]; duplicate {
				return nil, oops.In("snapshot.restore").
					Code("restore_failed").
					With("operation", "import_environment", "reason", "ambiguous_enumeration").
					Wrap(model.ErrInvalid)
			}

			content, present := entry.Value.Value.Get()
			if !present && !entry.Value.Unset {
				return nil, oops.In("snapshot.restore").
					Code("restore_failed").
					With("operation", "import_environment", "reason", "value_unavailable").
					Wrap(model.ErrInvalid)
			}

			seen[entry.Name] = struct{}{}

			observed[environmentKey{name: entry.Name, hidden: hidden}] = environmentShape{
				value:  content,
				unset:  entry.Value.Unset,
				hidden: hidden,
			}
		}
	}

	return observed, nil
}

func applyEnvironment(
	ctx context.Context,
	expected []model.Variable,
	targetGlobal map[environmentKey]environmentShape,
	scope gotmux.EnvironmentScope,
) (int, bool, error) {
	live, err := environmentTable(ctx, scope)
	if err != nil {
		return 0, false, err
	}

	applied := 0

	for _, variable := range expected {
		name := string(variable.Name)

		change, err := environmentChange(ctx, scope, name, variable, live)
		if err != nil {
			return applied, false, err
		}

		if change {
			applied++
		}
	}

	maskNames := environmentMaskNames(expected, live, targetGlobal)

	masked, err := applyEnvironmentMasks(ctx, scope, live, maskNames)
	if err != nil {
		return applied, false, err
	}

	applied += masked

	return applied, len(maskNames) > 0, nil
}

func environmentMaskNames(
	expected []model.Variable,
	live,
	targetGlobal map[environmentKey]environmentShape,
) map[string]struct{} {
	wantedNames := make(map[string]struct{}, len(expected))
	for _, variable := range expected {
		wantedNames[string(variable.Name)] = struct{}{}
	}

	maskNames := map[string]struct{}{}

	for key := range live {
		if _, ok := wantedNames[key.name]; !ok {
			maskNames[key.name] = struct{}{}
		}
	}

	for key := range targetGlobal {
		if _, ok := wantedNames[key.name]; !ok {
			maskNames[key.name] = struct{}{}
		}
	}

	return maskNames
}

func applyEnvironmentMasks(
	ctx context.Context,
	scope gotmux.EnvironmentScope,
	live map[environmentKey]environmentShape,
	maskNames map[string]struct{},
) (int, error) {
	applied := 0

	for _, name := range slices.Sorted(maps.Keys(maskNames)) {
		if shape, ok := live[environmentKey{name: name, hidden: false}]; ok && shape.unset {
			continue
		}

		if err := environmentFailure("remove", scope.Remove(ctx, name)); err != nil {
			return applied, oops.In("snapshot.restore").
				Code("restore_failed").
				With("operation", "import_environment", "reason", "environment_change_failed").
				Wrap(err)
		}

		applied++
	}

	return applied, nil
}

func environmentChange(
	ctx context.Context,
	scope gotmux.EnvironmentScope,
	name string,
	variable model.Variable,
	live map[environmentKey]environmentShape,
) (bool, error) {
	var (
		changed bool
		err     error
	)

	switch variable.State {
	case "set":
		changed, err = setEnvironment(ctx, scope, name, variable, live)
	case "removed":
		changed, err = removeEnvironment(ctx, scope, name, variable, live)
	case "absent":
		changed, err = unsetEnvironment(ctx, scope, name, live)
	default:
		return false, oops.In("snapshot.restore").
			Code("snapshot_invalid").
			With("operation", "import_environment", "reason", "variable_state_unsupported").
			Wrap(model.ErrInvalid)
	}

	if err != nil {
		return false, oops.In("snapshot.restore").
			Code("restore_failed").
			With("operation", "import_environment", "reason", "environment_change_failed").
			Wrap(err)
	}

	return changed, nil
}

func setEnvironment(ctx context.Context, scope gotmux.EnvironmentScope, name string, variable model.Variable, live map[environmentKey]environmentShape) (bool, error) {
	current, present := live[environmentKey{name: name, hidden: variable.Hidden}]
	if present && !current.unset && current.value == string(variable.Value) {
		return false, nil
	}

	if variable.Hidden {
		return true, environmentFailure("set_hidden", scope.SetHidden(ctx, name, string(variable.Value)))
	}

	return true, environmentFailure("set", scope.Set(ctx, name, string(variable.Value)))
}

func removeEnvironment(ctx context.Context, scope gotmux.EnvironmentScope, name string, variable model.Variable, live map[environmentKey]environmentShape) (bool, error) {
	current, present := live[environmentKey{name: name, hidden: variable.Hidden}]
	if present && current.unset {
		return false, nil
	}

	return true, environmentFailure("remove", scope.Remove(ctx, name))
}

func unsetEnvironment(ctx context.Context, scope gotmux.EnvironmentScope, name string, live map[environmentKey]environmentShape) (bool, error) {
	_, visible := live[environmentKey{name: name, hidden: false}]
	_, hidden := live[environmentKey{name: name, hidden: true}]

	if !visible && !hidden {
		return false, nil
	}

	return true, environmentFailure("unset", scope.Unset(ctx, name))
}

func environmentFailure(operation string, err error) error {
	if err == nil {
		return nil
	}

	return oops.In("snapshot.restore").
		Code("restore_failed").
		With("operation", "import_environment", "reason", "environment_"+operation+"_failed").
		Wrap(err)
}
