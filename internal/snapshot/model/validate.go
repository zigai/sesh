package model

import (
	"bytes"
	"encoding/hex"
	"slices"

	"github.com/samber/oops"
)

const maxDimension = 1 << 20

func ValidID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}

	for _, char := range id {
		if char != '-' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}

	return true
}

func (m Manifest) Validate() error {
	if m.Format != "sesh" {
		return oops.In("snapshot.model").Code("snapshot_invalid").With("operation", "validate", "phase", "format").Wrap(ErrInvalid)
	}

	if m.Version != Schema {
		return oops.In("snapshot.model").Code("snapshot_version_unsupported").With("operation", "validate", "phase", "version").Wrap(ErrVersion)
	}

	if m.Started <= 0 || m.Finished < m.Started || m.Source.TmuxVersion == "" || m.Source.LibraryVersion == "" || !validEndpointScope(m.Source.EndpointScope) {
		return oops.In("snapshot.model").Code("snapshot_invalid").With("operation", "validate", "phase", "provenance").Wrap(ErrInvalid)
	}

	checks := []struct {
		phase    string
		validate func() error
	}{
		{"bounds", m.validateBounds},
		{"evidence", m.validateEvidence},
		{"sessions", m.validateSessions},
		{"windows", m.validateWindows},
		{"links", m.validateLinks},
		{"groups", m.validateGroups},
		{"artifacts", m.validateArtifacts},
	}
	for _, check := range checks {
		if err := check.validate(); err != nil {
			return oops.In("snapshot.model").Code("snapshot_invalid").With("operation", "validate", "phase", check.phase).Wrap(err)
		}
	}

	return nil
}

func (m Manifest) Complete() bool {
	if m.Validate() != nil || m.Source.Consistency != "checkpoint" {
		return false
	}

	for _, evidence := range m.Evidence {
		if evidence.Status != "verified" {
			return false
		}
	}

	return true
}

func (m Manifest) validateBounds() error {
	total := len(m.Sessions) + len(m.Groups) + len(m.Windows) + len(m.Links) + len(m.Panes) + len(m.Artifacts)
	if total > MaxEntities || len(m.Sessions) == 0 || len(m.Windows) == 0 || len(m.Panes) == 0 {
		return ErrInvalid
	}

	return validateSettings(m.Source.Options, m.Source.Environment)
}

func (m Manifest) validateEvidence() error {
	domains := []string{"S01", "S02", "S03", "S04", "S05", "S06", "S07", "S08", "S09", "S10"}
	if len(m.Evidence) != len(domains) || !slices.Contains([]string{"observed", "checkpoint"}, m.Source.Consistency) {
		return ErrInvalid
	}

	seen := make(map[string]bool)
	for _, evidence := range m.Evidence {
		if seen[evidence.Domain] || !slices.Contains(domains, evidence.Domain) || !validEvidence(evidence) {
			return ErrInvalid
		}

		seen[evidence.Domain] = true
	}

	return nil
}

func validEvidence(e Evidence) bool {
	if e.Status == "verified" {
		return e.Reason == ""
	}

	if e.Status != "observed" && e.Status != "unverified" {
		return false
	}

	return slices.Contains([]Reason{
		ReasonNotCaptured, ReasonCaptureNotAtomic, ReasonTerminalCheckpointUnavailable,
		ReasonApplicationCheckpointUnavailable, ReasonOwnershipUnverified, ReasonObservedOnly,
	}, Reason(e.Reason))
}

func (m Manifest) validateSessions() error {
	ids, names := make(map[string]bool), make(map[string]bool)
	for _, session := range m.Sessions {
		if !ValidID(session.ID) || ids[session.ID] || len(session.Name) == 0 || names[string(session.Name)] {
			return ErrInvalid
		}

		if bytes.ContainsAny(session.Name, ".:\x00") || !validDirectory(session.Directory) {
			return ErrInvalid
		}

		if err := validateSettings(session.Options, session.Environment); err != nil {
			return err
		}

		ids[session.ID], names[string(session.Name)] = true, true
	}

	return nil
}

func (m Manifest) validateWindows() error {
	ids := make(map[string]bool)
	panes := make(map[string]bool)

	for _, window := range m.Windows {
		if !ValidID(window.ID) || ids[window.ID] || !validSize(window.Width, window.Height) {
			return ErrInvalid
		}

		ids[window.ID] = true
		if err := m.validateWindow(window, panes); err != nil {
			return err
		}
	}

	if len(panes) != len(m.Panes) {
		return ErrInvalid
	}

	return nil
}

func (m Manifest) validateWindow(window Window, seen map[string]bool) error {
	if window.Layout.Width != window.Width || window.Layout.Height != window.Height || window.Layout.Left != 0 || window.Layout.Top != 0 {
		return ErrInvalid
	}

	if err := validateSettings(window.Options, nil); err != nil {
		return err
	}

	leaves := make(map[string]Layout)
	if err := validateLayout(window.Layout, 0, leaves); err != nil {
		return err
	}

	return m.validateWindowPanes(window, leaves, seen)
}

func (m Manifest) validateWindowPanes(window Window, leaves map[string]Layout, seen map[string]bool) error {
	indexes := make(map[int]bool)
	active, count := 0, 0

	// Pane geometry depends on options such as pane-border-status, which a window often inherits.
	window.Options = m.EffectiveWindowOptions(window)

	for _, pane := range m.Panes {
		if pane.Window != window.ID {
			continue
		}

		if err := pane.validate(leaves, seen, indexes, window); err != nil {
			return err
		}

		if pane.Active {
			active++
		}

		count++
	}

	if count != len(leaves) || active != 1 {
		return ErrInvalid
	}

	return nil
}

func (p Pane) validate(leaves map[string]Layout, seen map[string]bool, indexes map[int]bool, window Window) error {
	leaf, exists := leaves[p.ID]
	if !exists || !ValidID(p.ID) || seen[p.ID] || p.Index < 0 || indexes[p.Index] {
		return ErrInvalid
	}

	if err := p.validateGeometry(leaf, window); err != nil {
		return err
	}

	if p.Terminal.History < 0 || p.Terminal.CursorX < 0 || p.Terminal.CursorY < 0 {
		return ErrInvalid
	}

	if err := validateLaunch(p.Launch); err != nil {
		return err
	}

	seen[p.ID], indexes[p.Index] = true, true

	return validateSettings(p.Options, nil)
}

func (p Pane) validateGeometry(leaf Layout, window Window) error {
	if !validSize(p.Width, p.Height) || p.Left < 0 || p.Top < 0 {
		return ErrInvalid
	}

	if err := p.validateDirectory(); err != nil {
		return err
	}

	leaf = terminalRectangle(leaf, window)
	if !window.Zoomed && (p.Width != leaf.Width || p.Height != leaf.Height || p.Left != leaf.Left || p.Top != leaf.Top) {
		return ErrInvalid
	}

	return nil
}

func (p Pane) validateDirectory() error {
	if !p.Dead && !validDirectory(p.Directory) {
		return ErrInvalid
	}

	if p.Dead && len(p.Directory) > 0 && !validDirectory(p.Directory) {
		return ErrInvalid
	}

	return nil
}

func terminalRectangle(leaf Layout, window Window) Layout {
	for _, option := range window.Options {
		if string(option.Name) != "pane-border-status" || !option.Present {
			continue
		}

		switch string(option.Value) {
		case "top":
			if leaf.Top == 0 {
				leaf.Top++
				leaf.Height--
			}
		case "bottom":
			if leaf.Top+leaf.Height == window.Height {
				leaf.Height--
			}
		}

		break
	}

	return leaf
}

func (m Manifest) validateArtifacts() error {
	ids := make(map[string]bool)

	var total int64

	for _, artifact := range m.Artifacts {
		if !ValidID(artifact.ID) || ids[artifact.ID] || artifact.Kind != "terminal-text" {
			return ErrInvalid
		}

		if !validDigest(artifact.SHA256) {
			return ErrInvalid
		}

		if artifact.Length < 0 || artifact.Length > MaxArtifactBytes {
			return ErrInvalid
		}

		total += artifact.Length
		ids[artifact.ID] = true
	}

	if total > MaxGenerationBytes {
		return ErrInvalid
	}

	return m.validateArtifactReferences(ids)
}

func (m Manifest) validateArtifactReferences(ids map[string]bool) error {
	referenced := make(map[string]bool)

	for _, pane := range m.Panes {
		id := pane.Terminal.Artifact
		if id == "" {
			continue
		}

		if !ids[id] {
			return ErrInvalid
		}

		referenced[id] = true
	}

	if len(referenced) != len(ids) {
		return ErrInvalid
	}

	return nil
}

func validDigest(value string) bool {
	digest, err := hex.DecodeString(value)
	return err == nil && len(digest) == 32 && hex.EncodeToString(digest) == value
}

func validSize(width, height int) bool {
	return width > 0 && height > 0 && width <= maxDimension && height <= maxDimension
}

func validDirectory(value Bytes) bool {
	return len(value) > 0 && value[0] == '/' && !bytes.ContainsRune(value, 0)
}
