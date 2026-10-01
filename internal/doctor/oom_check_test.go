//go:build linux || darwin

package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

var (
	errTestScope   = errors.New("scope")
	errTestSystemd = errors.New("systemctl missing")
)

func oomService(unit string, unitErr error, policy string, policyErr error) *Service {
	service := NewService(Options{})
	service.unitOf = func(int) (string, error) { return unit, unitErr }
	service.oomPolicyOf = func(context.Context, string) (string, error) { return policy, policyErr }

	return service
}

func TestServiceOOMPolicyWarnsWhenOneKilledProcessStopsTheWholeUnit(t *testing.T) {
	t.Parallel()

	results := oomService("tmux.service", nil, "stop", nil).checkServiceOOMPolicy(t.Context(), 4242)
	if len(results) != 1 {
		t.Fatalf("expected one warning, got %+v", results)
	}

	result := results[0]
	if result.Check != CheckServiceOOMPolicy || result.Status != StatusWarning || result.Reason != ReasonUnsafe {
		t.Fatalf("unexpected result: %+v", result)
	}

	for _, want := range []string{"tmux.service", "OOMPolicy=stop"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("message missing %q: %s", want, result.Message)
		}
	}

	if !strings.Contains(result.Fix, "OOMPolicy=continue") || !strings.Contains(result.Fix, "systemctl --user edit tmux.service") {
		t.Fatalf("the fix must name the directive and the unit: %s", result.Fix)
	}
}

func TestServiceOOMPolicyStaysSilentWhenSafeOrUndeterminable(t *testing.T) {
	t.Parallel()

	for name, service := range map[string]*Service{
		"continue":          oomService("tmux.service", nil, "continue", nil),
		"kill":              oomService("tmux.service", nil, "kill", nil),
		"not in a service":  oomService("", errTestScope, "", nil),
		"policy unreadable": oomService("tmux.service", nil, "", errTestSystemd),
	} {
		if results := service.checkServiceOOMPolicy(t.Context(), 4242); len(results) != 0 {
			t.Fatalf("%s must add no result, got %+v", name, results)
		}
	}

	if results := oomService("tmux.service", nil, "stop", nil).checkServiceOOMPolicy(t.Context(), 0); len(results) != 0 {
		t.Fatalf("no server pid means no unit to inspect, got %+v", results)
	}
}
