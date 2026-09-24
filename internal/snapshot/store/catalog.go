//go:build linux || darwin

package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"

	"github.com/zigai/sesh/internal/snapshot/model"
)

const (
	catalogVersion  = 1
	maxCatalogBytes = 16 << 20
	maxEntries      = 10000
	minimumRetained = 5
	idBytes         = 16
)

const (
	rewriteStatePending   = "pending"
	rewriteStateCompleted = "completed"
	rewriteStateRetired   = "retired"
)

var (
	ErrCorrupt          = errors.New("invalid native repository")
	ErrUnavailable      = errors.New("snapshot generation unavailable")
	ErrConflict         = errors.New("repository operation conflict")
	ErrRecoveryRequired = errors.New("latest save requires explicit recovery")
	ErrUncertain        = errors.New("catalog publication durability uncertain")
)

type Catalog struct {
	Version     int          `json:"version"`
	Sequence    uint64       `json:"sequence"`
	Current     string       `json:"current"`
	Attempts    []Attempt    `json:"attempts"`
	Generations []Generation `json:"generations"`
	Operations  []Operation  `json:"operations"`
	Rewrites    []Rewrite    `json:"rewrites"`
}

type Rewrite struct {
	ID          string   `json:"id"`
	Source      string   `json:"source"`
	Replacement string   `json:"replacement"`
	State       string   `json:"state"`
	Sessions    []string `json:"sessions"`
	Artifacts   []string `json:"artifacts"`
}

type Attempt struct {
	ID             string `json:"id"`
	Sequence       uint64 `json:"sequence"`
	State          string `json:"state"`
	Reason         string `json:"reason"`
	ManifestSHA256 string `json:"manifestSha256"`
}

type Generation struct {
	ID             string `json:"id"`
	Sequence       uint64 `json:"sequence"`
	ManifestSHA256 string `json:"manifestSha256"`
	Complete       bool   `json:"complete"`
}

type Operation struct {
	ID      string      `json:"id"`
	Source  string      `json:"source"`
	Target  model.Bytes `json:"target"`
	State   string      `json:"state"`
	Actions []Action    `json:"actions"`
}

type Action struct {
	ID        string            `json:"id"`
	Kind      model.ActionKind  `json:"kind"`
	State     model.ActionState `json:"state"`
	RuntimeID model.Bytes       `json:"runtimeId"`
}

func (c Catalog) validate() error {
	if c.Version != catalogVersion || len(c.Attempts)+len(c.Generations)+len(c.Operations)+len(c.Rewrites) > maxEntries {
		return ErrCorrupt
	}

	if err := c.validateAttempts(); err != nil {
		return err
	}

	if err := c.validateGenerations(); err != nil {
		return err
	}

	if err := c.validateOperations(); err != nil {
		return err
	}

	return c.validateRewrites()
}

func (c Catalog) validateAttempts() error {
	seen := make(map[string]bool)

	var previous uint64
	for _, attempt := range c.Attempts {
		if !validID(attempt.ID) || seen[attempt.ID] || attempt.Sequence <= previous || attempt.Sequence > c.Sequence {
			return ErrCorrupt
		}

		if !validAttemptOutcome(attempt.State, attempt.Reason) {
			return ErrCorrupt
		}

		if err := attempt.validateDigest(); err != nil {
			return err
		}

		seen[attempt.ID], previous = true, attempt.Sequence
	}

	return nil
}

func (c Catalog) validateGenerations() error {
	seen := make(map[string]bool)
	for _, generation := range c.Generations {
		if !validID(generation.ID) || seen[generation.ID] || !validDigest(generation.ManifestSHA256) {
			return ErrCorrupt
		}

		index := c.attemptIndex(generation.ID)
		if index < 0 {
			return ErrCorrupt
		}

		attempt := c.Attempts[index]
		if attempt.Sequence != generation.Sequence || (attempt.State != "committed" && attempt.State != "incomplete") {
			return ErrCorrupt
		}

		if !attempt.matchesGeneration(generation) {
			return ErrCorrupt
		}

		seen[generation.ID] = true
	}

	if c.Current != "" && !seen[c.Current] {
		return ErrCorrupt
	}

	return nil
}

func (a Attempt) matchesGeneration(generation Generation) bool {
	return generation.Complete == (a.State == "committed") && generation.ManifestSHA256 == a.ManifestSHA256
}

func (c Catalog) validateOperations() error {
	seen := make(map[string]bool)
	for _, op := range c.Operations {
		if !validID(op.ID) || !validID(op.Source) || seen[op.ID] || len(op.Target) == 0 {
			return ErrCorrupt
		}

		if !slices.Contains([]string{"active", "complete", "abandoned"}, op.State) {
			return ErrCorrupt
		}

		if op.State == "active" && c.generationIndex(op.Source) < 0 {
			return ErrCorrupt
		}

		if err := validateActions(op.Actions); err != nil {
			return err
		}

		if op.State == "complete" && slices.ContainsFunc(op.Actions, func(a Action) bool { return a.State != "verified" }) {
			return ErrCorrupt
		}

		seen[op.ID] = true
	}

	return nil
}

func (a Attempt) validateDigest() error {
	if a.State == "committed" || a.State == "incomplete" {
		if !validDigest(a.ManifestSHA256) {
			return ErrCorrupt
		}
	} else if a.ManifestSHA256 != "" {
		return ErrCorrupt
	}

	return nil
}

func validAttemptOutcome(state, reason string) bool {
	switch state {
	case "pending":
		return reason == "capture_pending"
	case "committed":
		return reason == ""
	case "incomplete":
		return reason == "parity_incomplete"
	case "failed":
		return reason == "save_failed"
	default:
		return false
	}
}

func (c Catalog) attemptIndex(id string) int {
	return slices.IndexFunc(c.Attempts, func(a Attempt) bool { return a.ID == id })
}

func (c Catalog) generationIndex(id string) int {
	return slices.IndexFunc(c.Generations, func(g Generation) bool { return g.ID == id })
}

func (c Catalog) rewriteIndex(id string) int {
	return slices.IndexFunc(c.Rewrites, func(r Rewrite) bool { return r.ID == id })
}

func (c Catalog) validateRewrites() error {
	seen := make(map[string]bool)
	sources := make(map[string]bool)
	replacements := make(map[string]bool)
	pendingSources := make(map[string]bool)

	for _, rewrite := range c.Rewrites {
		if seen[rewrite.ID] || sources[rewrite.Source] || (rewrite.Replacement != "" && replacements[rewrite.Replacement]) {
			return ErrCorrupt
		}

		if err := c.validateRewriteIntent(rewrite); err != nil {
			return err
		}

		if err := c.validateRewriteState(rewrite, pendingSources); err != nil {
			return err
		}

		seen[rewrite.ID], sources[rewrite.Source] = true, true
		replacements[rewrite.Replacement] = true
	}

	for _, op := range c.Operations {
		if op.State == "active" && pendingSources[op.Source] {
			return ErrCorrupt
		}
	}

	return nil
}

func (c Catalog) validateRewriteIntent(rewrite Rewrite) error {
	if !validID(rewrite.ID) || !validID(rewrite.Source) {
		return ErrCorrupt
	}

	if len(rewrite.Sessions) > model.MaxEntities || len(rewrite.Artifacts) > model.MaxEntities {
		return ErrCorrupt
	}

	if !validCanonicalList(rewrite.Sessions) || !validCanonicalList(rewrite.Artifacts) {
		return ErrCorrupt
	}

	index := c.attemptIndex(rewrite.Source)
	if index < 0 || !collectionRetiredState(c.Attempts[index].State) {
		return ErrCorrupt
	}

	return nil
}

func (c Catalog) validateRewriteState(rewrite Rewrite, pendingSources map[string]bool) error {
	switch rewrite.State {
	case rewriteStatePending:
		return c.validatePendingRewrite(rewrite, pendingSources)
	case rewriteStateCompleted:
		return c.validateCompletedRewrite(rewrite)
	case rewriteStateRetired:
		return c.validateRetiredRewrite(rewrite)
	default:
		return ErrCorrupt
	}
}

func (c Catalog) validatePendingRewrite(rewrite Rewrite, pendingSources map[string]bool) error {
	if rewrite.Replacement != "" && (!validID(rewrite.Replacement) || rewrite.Replacement == rewrite.Source) {
		return ErrCorrupt
	}

	if c.generationIndex(rewrite.Source) < 0 || pendingSources[rewrite.Source] || c.attemptIndex(rewrite.Replacement) >= 0 {
		return ErrCorrupt
	}

	pendingSources[rewrite.Source] = true

	return nil
}

func (c Catalog) validateCompletedRewrite(rewrite Rewrite) error {
	if !validID(rewrite.Replacement) || rewrite.Replacement == rewrite.Source {
		return ErrCorrupt
	}

	index := c.attemptIndex(rewrite.Replacement)
	if index < 0 || c.generationIndex(rewrite.Source) >= 0 {
		return ErrCorrupt
	}

	if !collectionRetiredState(c.Attempts[index].State) || c.Attempts[index].Sequence <= c.Attempts[c.attemptIndex(rewrite.Source)].Sequence {
		return ErrCorrupt
	}

	return nil
}

func (c Catalog) validateRetiredRewrite(rewrite Rewrite) error {
	if rewrite.Replacement != "" || c.generationIndex(rewrite.Source) >= 0 {
		return ErrCorrupt
	}

	return nil
}

func validCanonicalList(items []string) bool {
	for i, item := range items {
		if !model.ValidID(item) {
			return false
		}

		if i > 0 && item <= items[i-1] {
			return false
		}
	}

	return true
}

func newID() string {
	value := make([]byte, idBytes)
	rand.Read(value)

	return hex.EncodeToString(value)
}

func validID(id string) bool { return len(id) == idBytes*2 && model.ValidID(id) && validHex(id) }

func validHex(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validDigest(value string) bool { return len(value) == 64 && validHex(value) }
