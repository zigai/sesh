package model

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestInvalidGroups(t *testing.T) {
	for name, mutate := range map[string]func(*Manifest){
		"dangling member":   func(m *Manifest) { m.Groups[0].Members[1] = "missing" },
		"duplicate member":  func(m *Manifest) { m.Groups[0].Members[1] = "s0" },
		"missing group":     func(m *Manifest) { m.Sessions[0].Group = "missing" },
		"two owners":        func(m *Manifest) { m.Groups = append(m.Groups, Group{ID: "g1", Members: []string{"s0"}}) },
		"different indexes": func(m *Manifest) { m.Links[1].Index++ },
		"empty group":       func(m *Manifest) { m.Groups[0].Members = nil },
	} {
		t.Run(name, func(t *testing.T) {
			m := fixture()
			mutate(&m)

			if _, err := Encode(m); !errors.Is(err, ErrInvalid) {
				t.Fatal("accepted invalid group", err)
			}
		})
	}
}

func TestExactSelectionAndSharedClosure(t *testing.T) {
	m := fixture()
	m.Sessions[0].Name, m.Sessions[1].Name = Bytes("1"), Bytes("10")
	m.Sessions = append(m.Sessions, Session{ID: "s2", Name: Bytes("100"), Directory: Bytes("/tmp")})
	m.Windows = append(m.Windows, Window{ID: "w1", Name: Bytes("unrelated"), Width: 80, Height: 24, Layout: Layout{Width: 80, Height: 24, Pane: "p1"}})
	m.Panes = append(m.Panes, Pane{ID: "p1", Window: "w1", Directory: Bytes("/tmp"), Width: 80, Height: 24, Active: true, Launch: Launch{Recovery: "unverified"}})

	m.Links = append(m.Links, Link{Session: "s2", Window: "w1", Index: 9, Active: true})
	for _, id := range []string{"s0", "s1"} {
		got, err := m.Closure([]string{id, id})
		if err != nil {
			t.Fatal(err)
		}

		if diff := cmp.Diff([]string{"s0", "s1"}, got); diff != "" {
			t.Fatal(diff)
		}
	}

	if _, err := m.Closure([]string{"1"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("name adopted as graph identity", err)
	}
}
