package model

import (
	"errors"
	"testing"
)

func TestNativeResumeRequiresExecutableArgumentsAndDirectory(t *testing.T) {
	for _, missing := range []string{"none", "executable", "arguments", "directory"} {
		t.Run(missing, func(t *testing.T) {
			m := fixture()
			launch := &m.Panes[0].Launch
			launch.Recovery = "resume"
			launch.Executable = Bytes("/usr/bin/codex")
			launch.Arguments = []Bytes{Bytes("codex"), Bytes("resume"), Bytes("native-session")}
			launch.Directory = Bytes("/tmp")

			switch missing {
			case "executable":
				launch.Executable = nil
			case "arguments":
				launch.Arguments = nil
			case "directory":
				launch.Directory = nil
			}

			err := m.Validate()
			if missing == "none" {
				if err != nil {
					t.Fatal(err)
				}

				if m.Complete() {
					t.Fatal("native resume metadata claimed full application recovery")
				}
			} else if !errors.Is(err, ErrInvalid) {
				t.Fatalf("incomplete native resume accepted: %v", err)
			}
		})
	}
}

func TestNonrestartableLaunchValidation(t *testing.T) {
	cases := []struct {
		name      string
		recovery  Recovery
		reason    Reason
		exe       Bytes
		args      []Bytes
		dir       Bytes
		wantValid bool
	}{
		{"valid pane_dead", RecoveryNonrestartable, ReasonPaneDead, nil, nil, nil, true},
		{"valid pane_dead with dir", RecoveryNonrestartable, ReasonPaneDead, nil, nil, Bytes("/tmp"), true},
		{"valid executable_deleted", RecoveryNonrestartable, ReasonExecutableDeleted, Bytes("/bin/deleted"), []Bytes{Bytes("deleted")}, Bytes("/tmp"), true},
		{"valid working_directory_deleted", RecoveryNonrestartable, ReasonWorkingDirectoryDeleted, Bytes("/bin/sh"), []Bytes{Bytes("sh")}, Bytes("/tmp/deleted"), true},
		{"empty reason", RecoveryNonrestartable, "", nil, nil, nil, false},
		{"unknown reason", RecoveryNonrestartable, "unknown_reason", nil, nil, nil, false},
		{"restart with reason", RecoveryRestart, ReasonPaneDead, Bytes("/bin/sh"), []Bytes{Bytes("sh")}, Bytes("/tmp"), false},
		{"resume with reason", RecoveryResume, ReasonPaneDead, Bytes("/bin/sh"), []Bytes{Bytes("sh")}, Bytes("/tmp"), false},
		{"unverified with nonrestartable reason", RecoveryUnverified, ReasonPaneDead, nil, nil, nil, false},
		{"unverified unsupported application", RecoveryUnverified, "unsupported_application", Bytes("/usr/bin/node"), []Bytes{Bytes("node"), Bytes("server.js")}, Bytes("/tmp"), true},
		{"unverified foreground unavailable", RecoveryUnverified, "foreground_unavailable", Bytes("/bin/sh"), []Bytes{Bytes("sh")}, Bytes("/tmp"), true},
		{"unverified unknown reason", RecoveryUnverified, "unknown_reason", nil, nil, nil, false},
		{"relative dir in nonrestartable", RecoveryNonrestartable, ReasonPaneDead, nil, nil, Bytes("relative"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := fixture()
			launch := &m.Panes[0].Launch
			launch.Recovery = tc.recovery
			launch.Reason = string(tc.reason)
			launch.Executable = tc.exe
			launch.Arguments = tc.args
			launch.Directory = tc.dir

			err := m.Validate()
			if tc.wantValid && err != nil {
				t.Fatalf("expected valid, got error: %v", err)
			}

			if !tc.wantValid && !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got: %v", err)
			}
		})
	}
}

func TestDeadPaneDirectoryValidation(t *testing.T) {
	t.Run("dead pane with empty directory is valid", func(t *testing.T) {
		m := fixture()
		m.Panes[0].Dead = true
		m.Panes[0].Directory = nil
		m.Panes[0].Launch.Recovery = RecoveryNonrestartable
		m.Panes[0].Launch.Reason = string(ReasonPaneDead)

		if err := m.Validate(); err != nil {
			t.Fatalf("expected valid dead pane without directory, got: %v", err)
		}
	})

	t.Run("live pane with empty directory is invalid", func(t *testing.T) {
		m := fixture()
		m.Panes[0].Dead = false
		m.Panes[0].Directory = nil

		if err := m.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("expected live pane without directory to fail, got: %v", err)
		}
	})

	t.Run("dead pane with relative directory is invalid", func(t *testing.T) {
		m := fixture()
		m.Panes[0].Dead = true
		m.Panes[0].Directory = Bytes("relative/path")
		m.Panes[0].Launch.Recovery = RecoveryNonrestartable
		m.Panes[0].Launch.Reason = string(ReasonPaneDead)

		if err := m.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("expected dead pane with relative directory to fail, got: %v", err)
		}
	})
}
