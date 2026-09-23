package tui

import "testing"

func TestDeletePathNodeBackward(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		cursor     int
		wantValue  string
		wantCursor int
	}{
		{name: "absolute trailing slash", value: "/work/project/api/", cursor: len([]rune("/work/project/api/")), wantValue: "/work/project/", wantCursor: len([]rune("/work/project/"))},
		{name: "absolute no trailing slash", value: "/work/project/api", cursor: len([]rune("/work/project/api")), wantValue: "/work/project/", wantCursor: len([]rune("/work/project/"))},
		{name: "relative trailing slash", value: "work/project/", cursor: len([]rune("work/project/")), wantValue: "work/", wantCursor: len([]rune("work/"))},
		{name: "single node", value: "project", cursor: len([]rune("project")), wantValue: "", wantCursor: 0},
		{name: "root child", value: "/project/", cursor: len([]rune("/project/")), wantValue: "/", wantCursor: 1},
		{name: "middle cursor", value: "/work/project/api", cursor: len([]rune("/work/pro")), wantValue: "/work/ject/api", wantCursor: len([]rune("/work/"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotValue, gotCursor := deletePathNodeBackward(tt.value, tt.cursor)
			if gotValue != tt.wantValue || gotCursor != tt.wantCursor {
				t.Fatalf("deletePathNodeBackward(%q, %d) = %q/%d, want %q/%d", tt.value, tt.cursor, gotValue, gotCursor, tt.wantValue, tt.wantCursor)
			}
		})
	}
}
