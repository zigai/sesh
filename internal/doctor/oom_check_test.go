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

func oomService(serverPID int, unit string, unitErr error, policy string, policyErr error) *Service {
	service := NewService(Options{ServiceDirs: []string{}})
	service.serverPID = serverPID
	service.unitOf = func(int) (string, error) { return unit, unitErr }
	service.oomPolicyOf = func(context.Context, string) (string, error) { return policy, policyErr }

	return service
}

func TestServiceOOMPolicyWarnsWhenOneKilledProcessStopsTheWholeUnit(t *testing.T) {
	t.Parallel()

	result := oomService(4242, "tmux.service", nil, "stop", nil).checkServiceOOMPolicy(t.Context())
	if result.ID != CheckServiceOOMPolicy || result.Status != StatusWarn || result.Reason != ReasonUnsafe {
		t.Fatalf("unexpected result: %+v", result)
	}

	for _, want := range []string{"tmux.service", "OOMPolicy=stop"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("message missing %q: %s", want, result.Message)
		}
	}

	if result.Fix == nil || !strings.Contains(result.Fix.Manual, "OOMPolicy=continue") ||
		!strings.Contains(result.Fix.Manual, "systemctl --user edit tmux.service") {
		t.Fatalf("the fix must name the directive and the unit: %+v", result.Fix)
	}
}

func TestServiceOOMPolicyPassesWhenOtherPoliciesKeepTheServer(t *testing.T) {
	t.Parallel()

	for _, policy := range []string{"continue", "kill"} {
		result := oomService(4242, "tmux.service", nil, policy, nil).checkServiceOOMPolicy(t.Context())
		if result.Status != StatusOK || !strings.Contains(result.Message, "OOMPolicy="+policy) {
			t.Fatalf("policy %s: %+v", policy, result)
		}
	}
}

func TestServiceOOMPolicySkipsWhenTheUnitCannotBeDetermined(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		service *Service
		needs   string
	}{
		"not in a service":  {service: oomService(4242, "", errTestScope, "", nil)},
		"policy unreadable": {service: oomService(4242, "tmux.service", nil, "", errTestSystemd)},
		"no server":         {service: oomService(0, "tmux.service", nil, "stop", nil), needs: CheckTmuxServer},
		"not systemd":       {service: NewService(Options{})},
	} {
		result := tc.service.checkServiceOOMPolicy(t.Context())
		if result.Status != StatusSkip || result.Needs != tc.needs || result.Message == "" {
			t.Fatalf("%s: want a skip with a reason, got %+v", name, result)
		}
	}
}
