//go:build linux

package doctor

import (
	"errors"
	"testing"
)

func TestUnitFromCgroupDistinguishesNonMembershipFromInspectionFailure(test *testing.T) {
	test.Parallel()

	for _, data := range []string{"", "malformed", "0::relative.scope", "1:name=systemd:/legacy.scope"} {
		_, err := unitFromCgroup([]byte(data))
		if err == nil || errors.Is(err, errNoServiceUnit) {
			test.Errorf("uninspected cgroup %q was treated as known non-membership: %v", data, err)
		}
	}
}

func TestUnitFromCgroupNamesOnlyAServiceThatDirectlyHoldsTheProcess(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		cgroup string
		want   string
		wantOK bool
	}{
		"service":           {"0::/user.slice/user-1000.slice/user@1000.service/app.slice/tmux.service\n", "tmux.service", true},
		"terminal scope":    {"0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-alacritty-42.scope\n", "", false},
		"user manager only": {"0::/user.slice/user-1000.slice/user@1000.service/init.scope\n", "", false},
		"empty":             {"", "", false},
	} {
		got, err := unitFromCgroup([]byte(test.cgroup))
		if (err == nil) != test.wantOK || got != test.want {
			t.Fatalf("%s: got %q, %v; want %q (ok=%t)", name, got, err, test.want, test.wantOK)
		}
	}
}
