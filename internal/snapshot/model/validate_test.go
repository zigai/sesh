package model

import (
	"testing"
)

func TestIncompleteEvidenceCannotAuthorizeRestore(t *testing.T) {
	m := fixture()

	m.Source.Consistency = "checkpoint"
	for i := range m.Evidence {
		m.Evidence[i].Status, m.Evidence[i].Reason = "verified", ""
	}

	if !m.Complete() {
		t.Fatal("invalid synthetic complete fixture")
	}

	for i := range m.Evidence {
		m.Evidence[i].Status, m.Evidence[i].Reason = "unverified", "not_captured"
		if m.Complete() {
			t.Fatalf("missing evidence for %s was accepted", m.Evidence[i].Domain)
		}

		m.Evidence[i].Status, m.Evidence[i].Reason = "verified", ""
	}

	m.Source.Consistency = "observed"
	if m.Complete() {
		t.Fatal("matching observations authorized full restoration")
	}
}

func TestPracticalRecoveryReadyKeepsExactCompletenessSeparate(t *testing.T) {
	m := fixture()

	if m.Complete() {
		t.Fatal("observational fixture unexpectedly reports exact completeness")
	}

	if !m.PracticalRecoveryReady() {
		t.Fatal("valid observational graph should be practical recovery material")
	}

	m.Links[0].Window = "missing"
	if m.PracticalRecoveryReady() {
		t.Fatal("invalid graph should not be practical recovery material")
	}
}
