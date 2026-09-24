//go:build (linux || darwin) && integration

package restore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/zigai/sesh/internal/snapshot/model"
	"github.com/zigai/sesh/internal/snapshot/store"
)

var (
	errJournalOrder     = errors.New("test journal: transition not allowed from this state")
	errJournalAction    = errors.New("test journal: unknown action")
	errJournalActionSet = errors.New("test journal: action set mismatch")
	errJournalKind      = errors.New("test journal: action kind mismatch")
)

type TestStep struct {
	State     model.ActionState
	RuntimeID string
}

type memoryJournal struct {
	mu     sync.Mutex
	states map[string]TestStep
	calls  []string

	failIntent      map[string]error
	failConfirm     map[string]error
	failNotSent     map[string]error
	failUnknown     map[string]error
	failSteps       error
	beforeConfirmed func(step Step) error
	beforeIntent    func(step Step) error
	confirmCount    map[string]int
}

func newMemoryJournal() *memoryJournal {
	return &memoryJournal{
		mu:              sync.Mutex{},
		states:          map[string]TestStep{},
		calls:           nil,
		failIntent:      map[string]error{},
		failConfirm:     map[string]error{},
		failNotSent:     map[string]error{},
		failUnknown:     map[string]error{},
		failSteps:       nil,
		beforeConfirmed: nil,
		beforeIntent:    nil,
		confirmCount:    map[string]int{},
	}
}

func (j *memoryJournal) state(id string) TestStep {
	state, ok := j.states[id]
	if !ok {
		return TestStep{State: StatePlanned, RuntimeID: ""}
	}

	return state
}

func (j *memoryJournal) set(id string, state TestStep) {
	j.states[id] = state
}

func (j *memoryJournal) Steps(_ context.Context, steps []Step) (map[string]StepState, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.calls = append(j.calls, "steps")

	if j.failSteps != nil {
		return nil, j.failSteps
	}

	states := make(map[string]StepState, len(steps))
	for _, step := range steps {
		states[step.ID()] = StepState(j.state(step.ID()))
	}

	return states, nil
}

func (j *memoryJournal) Intent(_ context.Context, step Step) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.calls = append(j.calls, "intent:"+step.ID())

	if hook := j.beforeIntent; hook != nil {
		if err := hook(step); err != nil {
			return err
		}
	}

	if err := j.failIntent[step.ID()]; err != nil {
		return err
	}

	current := j.state(step.ID())

	switch current.State {
	case StatePlanned:
		j.set(step.ID(), TestStep{State: StateIssued, RuntimeID: ""})
	case StateNotSent:
		j.set(step.ID(), TestStep{State: StateIssued, RuntimeID: ""})
	case StateIssued:
		return nil
	default:
		return fmt.Errorf("%w: intent from %s", errJournalOrder, current.State)
	}

	return nil
}

func (j *memoryJournal) Confirmed(_ context.Context, step Step, runtimeID string) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.calls = append(j.calls, "confirmed:"+step.ID())
	j.confirmCount[step.ID()]++

	if hook := j.beforeConfirmed; hook != nil {
		if err := hook(step); err != nil {
			return err
		}
	}

	if err := j.failConfirm[step.ID()]; err != nil {
		return err
	}

	if j.state(step.ID()).State != StateIssued {
		return fmt.Errorf("%w: confirm from %s", errJournalOrder, j.state(step.ID()).State)
	}

	j.set(step.ID(), TestStep{State: StateConfirmed, RuntimeID: runtimeID})

	return nil
}

func (j *memoryJournal) NotSent(_ context.Context, step Step) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.calls = append(j.calls, "not-sent:"+step.ID())

	if err := j.failNotSent[step.ID()]; err != nil {
		return err
	}

	if j.state(step.ID()).State != StateIssued {
		return fmt.Errorf("%w: not-sent from %s", errJournalOrder, j.state(step.ID()).State)
	}

	j.set(step.ID(), TestStep{State: StateNotSent, RuntimeID: ""})

	return nil
}

func (j *memoryJournal) Unknown(_ context.Context, step Step) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.calls = append(j.calls, "unknown:"+step.ID())

	if err := j.failUnknown[step.ID()]; err != nil {
		return err
	}

	if j.state(step.ID()).State != StateIssued {
		return nil
	}

	j.set(step.ID(), TestStep{State: StateUnknown, RuntimeID: ""})

	return nil
}

type storeJournal struct {
	repository *store.Repository
	operation  string
	states     map[string]store.Action
}

func newStoreJournal(ctx context.Context, repository *store.Repository, operation string) (*storeJournal, error) {
	op, err := repository.ReconcileOperation(ctx, operation)
	if err != nil {
		return nil, fmt.Errorf("read operation journal: %w", err)
	}

	states := make(map[string]store.Action, len(op.Actions))
	for _, action := range op.Actions {
		states[action.ID] = action
	}

	return &storeJournal{repository: repository, operation: operation, states: states}, nil
}

func (j *storeJournal) transition(ctx context.Context, id string, expected, next model.ActionState, runtimeID model.Bytes) error {
	if err := j.repository.Transition(ctx, j.operation, id, expected, next, runtimeID); err != nil {
		return fmt.Errorf("record action transition: %w", err)
	}

	action := j.states[id]
	action.State = next
	action.RuntimeID = runtimeID
	j.states[id] = action

	return nil
}

func (j *storeJournal) advance(ctx context.Context, id string) error {
	action, ok := j.states[id]
	if !ok {
		return fmt.Errorf("%w: %s", errJournalAction, id)
	}

	switch action.State {
	case StateIssued:
		return nil
	case StatePlanned:
		return j.transition(ctx, id, StatePlanned, StateIssued, nil)
	case StateNotSent:
		if err := j.transition(ctx, id, StateNotSent, StatePlanned, nil); err != nil {
			return err
		}

		return j.transition(ctx, id, StatePlanned, StateIssued, nil)
	default:
		return ErrPriorUncertainty
	}
}

func (j *storeJournal) Steps(_ context.Context, steps []Step) (map[string]StepState, error) {
	states := make(map[string]StepState, len(steps))

	for _, step := range steps {
		action, ok := j.states[ActionID(step.ID())]
		if !ok {
			return nil, errJournalActionSet
		}

		if action.Kind != StepKindAction(step) {
			return nil, errJournalKind
		}

		states[step.ID()] = StepState{State: action.State, RuntimeID: string(action.RuntimeID)}
	}

	return states, nil
}

func (j *storeJournal) Intent(ctx context.Context, step Step) error {
	return j.advance(ctx, ActionID(step.ID()))
}

func (j *storeJournal) Confirmed(ctx context.Context, step Step, runtimeID string) error {
	return j.transition(ctx, ActionID(step.ID()), StateIssued, StateConfirmed, model.Bytes(runtimeID))
}

func (j *storeJournal) NotSent(ctx context.Context, step Step) error {
	return j.transition(ctx, ActionID(step.ID()), StateIssued, StateNotSent, nil)
}

func (j *storeJournal) Unknown(ctx context.Context, step Step) error {
	action, ok := j.states[ActionID(step.ID())]
	if !ok {
		return errJournalActionSet
	}

	if action.State != StateIssued {
		return nil
	}

	return j.transition(ctx, ActionID(step.ID()), StateIssued, StateUnknown, nil)
}

type faultJournal struct {
	inner Journal

	mu              sync.Mutex
	calls           []string
	confirmCount    map[string]int
	failIntent      map[string]error
	failConfirmed   map[string]error
	beforeIntent    func(step Step) error
	beforeConfirmed func(step Step) error
}

func newFaultJournal(inner Journal) *faultJournal {
	return &faultJournal{
		inner:           inner,
		mu:              sync.Mutex{},
		calls:           nil,
		confirmCount:    map[string]int{},
		failIntent:      map[string]error{},
		failConfirmed:   map[string]error{},
		beforeIntent:    nil,
		beforeConfirmed: nil,
	}
}

func (j *faultJournal) record(call string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.calls = append(j.calls, call)
}

func (j *faultJournal) Steps(ctx context.Context, steps []Step) (map[string]StepState, error) {
	j.record("steps")

	states, err := j.inner.Steps(ctx, steps)
	if err != nil {
		return nil, fmt.Errorf("read delegate journal: %w", err)
	}

	return states, nil
}

func (j *faultJournal) Intent(ctx context.Context, step Step) error {
	j.record("intent:" + step.ID())

	if hook := j.beforeIntent; hook != nil {
		if err := hook(step); err != nil {
			return err
		}
	}

	if err := j.failIntent[step.ID()]; err != nil {
		return err
	}

	if err := j.inner.Intent(ctx, step); err != nil {
		return fmt.Errorf("record delegate intent: %w", err)
	}

	return nil
}

func (j *faultJournal) Confirmed(ctx context.Context, step Step, runtimeID string) error {
	j.record("confirmed:" + step.ID())

	j.mu.Lock()
	j.confirmCount[step.ID()]++
	j.mu.Unlock()

	if hook := j.beforeConfirmed; hook != nil {
		if err := hook(step); err != nil {
			return err
		}
	}

	if err := j.failConfirmed[step.ID()]; err != nil {
		return err
	}

	if err := j.inner.Confirmed(ctx, step, runtimeID); err != nil {
		return fmt.Errorf("record delegate confirmation: %w", err)
	}

	return nil
}

func (j *faultJournal) NotSent(ctx context.Context, step Step) error {
	j.record("not-sent:" + step.ID())

	if err := j.inner.NotSent(ctx, step); err != nil {
		return fmt.Errorf("record delegate refusal: %w", err)
	}

	return nil
}

func (j *faultJournal) Unknown(ctx context.Context, step Step) error {
	j.record("unknown:" + step.ID())

	if err := j.inner.Unknown(ctx, step); err != nil {
		return fmt.Errorf("record delegate uncertainty: %w", err)
	}

	return nil
}

func testOperationID(label string) string {
	digest := sha256.Sum256([]byte(label))

	return hex.EncodeToString(digest[:16])
}

func testActions(specs []ActionSpec) []store.Action {
	actions := make([]store.Action, 0, len(specs))
	for _, spec := range specs {
		actions = append(actions, store.Action{ID: spec.ID, Kind: spec.Kind, State: "planned", RuntimeID: nil})
	}

	return actions
}
