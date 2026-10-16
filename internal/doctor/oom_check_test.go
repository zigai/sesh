//go:build linux || darwin

package doctor

import (
	"context"
	"errors"
	"fmt"
	"runtime"
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

func TestServiceOOMPolicyReportsApplicablePoliciesAndInspectionErrors(test *testing.T) {
	test.Parallel()

	for _, scenario := range []struct {
		name    string
		service *Service
		status  Status
		reason  string
		needs   string
		message string
		fix     string
	}{
		{"stops whole unit", oomService(4242, "tmux.service", nil, "stop", nil), StatusWarn, ReasonUnsafe, "", "tmux.service uses OOMPolicy=stop", "OOMPolicy=continue"},
		{"continues", oomService(4242, "tmux.service", nil, "continue", nil), StatusOK, "", "", "tmux.service uses OOMPolicy=continue", ""},
		{"kills", oomService(4242, "tmux.service", nil, "kill", nil), StatusOK, "", "", "tmux.service uses OOMPolicy=kill", ""},
		{"membership error", oomService(4242, "", errTestScope, "", nil), StatusWarn, ReasonInspectionFailed, "", "could not be inspected", "cgroup"},
		{"policy error", oomService(4242, "tmux.service", nil, "", errTestSystemd), StatusWarn, ReasonInspectionFailed, "", "unreadable", "systemctl --user show"},
		{"no server", oomService(0, "tmux.service", nil, "stop", nil), StatusSkip, "", CheckTmuxServer, "unavailable", ""},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			test.Parallel()
			results := scenario.service.checkServiceOOMPolicy(test.Context())
			if runtime.GOOS != "linux" {
				if len(results) != 0 {
					test.Fatalf("unsupported OOM check collected: %+v", results)
				}
				return
			}
			if len(results) != 1 {
				test.Fatalf("applicable OOM check omitted: %+v", results)
			}
			result := results[0]
			if result.ID != CheckServiceOOMPolicy || result.Status != scenario.status || result.Reason != scenario.reason || result.Needs != scenario.needs || !strings.Contains(result.Message, scenario.message) {
				test.Fatalf("unexpected OOM check: %+v", result)
			}
			if scenario.fix == "" {
				if result.Fix != nil {
					test.Fatalf("unexpected repair: %+v", result.Fix)
				}
			} else if result.Fix == nil || !strings.Contains(result.Fix.Command+result.Fix.Manual, scenario.fix) {
				test.Fatalf("missing actionable repair %q: %+v", scenario.fix, result)
			}
			if scenario.reason == ReasonUnsafe && !strings.Contains(result.Fix.Manual, "systemctl --user edit tmux.service") {
				test.Fatalf("unsafe OOM repair lost its unit: %+v", result.Fix)
			}
		})
	}
}

func TestServiceOOMPolicyOmitsKnownNonMembershipAndUnsupportedServices(test *testing.T) {
	test.Parallel()

	for _, service := range []*Service{
		oomService(4242, "", fmt.Errorf("membership: %w", errNoServiceUnit), "", nil),
		NewService(Options{}),
	} {
		service.oomPolicyOf = func(context.Context, string) (string, error) {
			test.Error("inapplicable OOM policy was queried")
			return "", errTestSystemd
		}
		if results := service.checkServiceOOMPolicy(test.Context()); len(results) != 0 {
			test.Fatalf("inapplicable OOM check was collected: %+v", results)
		}
	}
}
