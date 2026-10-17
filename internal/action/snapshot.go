package action

import (
	"errors"
	"fmt"
)

const (
	CollisionSkip = "skip"
	CollisionFail = "fail"
)

var (
	ErrMixedGenerations = errors.New("selected saved sessions belong to different generations")

	SnapshotSave = define(Descriptor{
		ID:      "snapshot.save",
		Title:   "Snapshot session",
		Summary: "Save live tmux sessions to the native repository",
		Help:    "Save live tmux sessions to the native repository.\n\nSaves are practical recovery observations, not atomic checkpoints or application memory images.",
		Subject: SubjectLiveSession,
		Arity:   ArityMany,
		Params: []Param{
			newParam("process-enrichment", ParamBool, "Capture foreground process and native application recovery context").withDefault("true"),
			newParam("process-env", ParamList, "Environment keys to capture for observed foreground processes [default: snapshot.process_environment]"),
		},
		NeedsClient: false,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteOnByDefault,
		InPicker:    false,
		check:       nil,
	}, bindSaveSnapshot, func(out SavedSnapshot) string {
		return fmt.Sprintf("Saved snapshot %s (%s)", out.Generation, out.Status)
	})

	SnapshotRestore = define(Descriptor{
		ID:      "snapshot.restore",
		Title:   "Restore session",
		Summary: "Restore saved sessions onto tmux",
		Help: "Restore saved sessions from the native snapshot repository onto tmux.\n\n" +
			"Without session arguments every saved session of the generation is restored. Guards against live " +
			"session collisions, acquires endpoint leases and verifies structural fidelity.",
		Subject: SubjectSavedSession,
		Arity:   ArityOptional,
		Params: []Param{
			newParam("generation", ParamText, "Generation ID to restore [default: the current generation]"),
			newParam("operation", ParamText, "Restore under this operation ID; an existing operation resumes without replaying uncertain effects"),
			newParam("no-processes", ParamBool, "Do not launch processes recorded in launch context"),
			newParam("allow-missing-dir", ParamBool, "Fall back when a saved working directory does not exist"),
			newParam("on-collision", ParamChoice, "Handle saved sessions whose names are already live; unset, restoring all sessions skips them and naming sessions fails").
				withChoices(CollisionSkip, CollisionFail),
		},
		NeedsClient: false,
		Interactive: false,
		Destructive: false,
		Remote:      RemoteOnByDefault,
		InPicker:    false,
		check:       nil,
	}, bindRestoreSnapshot, func(out RestoredSnapshot) string {
		return fmt.Sprintf("Restored %d of %d sessions (%s)", len(out.Sessions), len(out.Sessions)+len(out.SessionsSkipped), out.Status)
	})
)

type SaveSnapshot struct {
	Sessions          []SessionRef `json:"sessions"`
	ProcessEnrichment bool         `json:"processEnrichment"`
	Environment       []string     `json:"environment"`
	EnvironmentSet    bool         `json:"environmentSet"`
}

type SavedSnapshot struct {
	Attempt    string `json:"attempt"`
	Generation string `json:"generation"`
	Status     string `json:"status"`
}

type RestoreSnapshot struct {
	Generation      string   `json:"generation"`
	Sessions        []string `json:"sessions"`
	Operation       string   `json:"operation"`
	LaunchProcesses bool     `json:"launchProcesses"`
	AllowMissingDir bool     `json:"allowMissingDir"`
	OnCollision     string   `json:"onCollision"`
}

type RestoredSnapshot struct {
	OperationID         string           `json:"operationId"`
	Generation          string           `json:"generation"`
	Status              string           `json:"status"`
	Reason              string           `json:"reason"`
	Sessions            []SessionRef     `json:"sessions"`
	Panes               []RestoredPane   `json:"panes"`
	Verified            bool             `json:"verified"`
	Uncertain           bool             `json:"uncertain"`
	MissingRequirements []Requirement    `json:"missingRequirements"`
	SessionsSkipped     []SkippedSession `json:"sessionsSkipped"`
	Advisories          []Mismatch       `json:"advisories"`
	Mismatches          []Mismatch       `json:"mismatches"`
}

type RestoredPane struct {
	PaneID             string `json:"paneId"`
	RuntimeID          string `json:"runtimeId"`
	Outcome            string `json:"outcome"`
	Reason             string `json:"reason"`
	AppVerified        bool   `json:"appVerified"`
	CWDFallback        bool   `json:"cwdFallback"`
	VerificationReason string `json:"verificationReason"`
	Location           string `json:"location"`
	Program            string `json:"program"`
}

type Requirement struct {
	Domain string `json:"domain"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type SkippedSession struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type Mismatch struct {
	Domain string `json:"domain"`
	Entity string `json:"entity"`
	Reason string `json:"reason"`
}

func bindSaveSnapshot(subject Subject, values Values) (SaveSnapshot, error) {
	refs := make([]SessionRef, 0, len(subject.Sessions))
	for _, target := range subject.Sessions {
		refs = append(refs, RefOf(target))
	}

	return SaveSnapshot{
		Sessions:          refs,
		ProcessEnrichment: values.Bool("process-enrichment"),
		Environment:       values.Strings("process-env"),
		EnvironmentSet:    values.Has("process-env"),
	}, nil
}

func bindRestoreSnapshot(subject Subject, values Values) (RestoreSnapshot, error) {
	generation := values.String("generation")
	sessions := make([]string, 0, len(subject.Sessions))

	for _, target := range subject.Sessions {
		if generation == "" {
			generation = target.Generation
		}

		if target.Generation != generation {
			return RestoreSnapshot{}, Wrap(CodeInvalidArgument, ErrMixedGenerations)
		}

		sessions = append(sessions, target.ID)
	}

	return RestoreSnapshot{
		Generation:      generation,
		Sessions:        sessions,
		Operation:       values.String("operation"),
		LaunchProcesses: !values.Bool("no-processes"),
		AllowMissingDir: values.Bool("allow-missing-dir"),
		OnCollision:     values.String("on-collision"),
	}, nil
}
