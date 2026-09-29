package display

import "testing"

func TestFieldMetadataDescribesProviderAndRowKind(t *testing.T) {
	cases := []struct {
		field    string
		provider FieldProvider
		bound    int
		session  bool
		agent    bool
	}{
		{field: FieldSessionName, provider: ProviderSession, bound: 0, session: true, agent: true},
		{field: FieldStatusAge, provider: ProviderClock, bound: 0, session: false, agent: true},
		{field: FieldAgentGitGlyph, provider: ProviderGit, bound: 1, session: false, agent: true},
		{field: FieldCurrentMarker, provider: ProviderSession, bound: markerCellWidth, session: true, agent: true},
	}

	for _, testCase := range cases {
		info, ok := FieldInfo(testCase.field)
		if !ok {
			t.Errorf("field %s has no metadata", testCase.field)
			continue
		}

		if info.Provider != testCase.provider || info.WidthBound != testCase.bound || info.SessionRows != testCase.session || info.AgentRows != testCase.agent {
			t.Errorf("field %s metadata = %+v", testCase.field, info)
		}
	}

	if _, ok := FieldInfo("unknown_field"); ok {
		t.Fatal("unknown field has metadata")
	}
}
