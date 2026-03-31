package sprint

import "testing"

func TestClassifyFastPath_LabelMatch(t *testing.T) {
	cases := []struct {
		labels []string
		want   bool
		desc   string
	}{
		{[]string{"dependency"}, true, "dependency label"},
		{[]string{"dependencies"}, true, "dependencies label"},
		{[]string{"test"}, true, "test label"},
		{[]string{"tests"}, true, "tests label"},
		{[]string{"documentation"}, true, "documentation label"},
		{[]string{"doc"}, true, "doc label"},
		{[]string{"docs"}, true, "docs label"},
		{[]string{"lint"}, true, "lint label"},
		{[]string{"good-first-issue"}, true, "good-first-issue label"},
		{[]string{"chore"}, true, "chore label"},
		{[]string{"formatting"}, true, "formatting label"},
		{[]string{"bug", "P1"}, false, "no fast-path label"},
		{[]string{}, false, "no labels"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := classifyFastPath("some issue title", tc.labels)
			if got != tc.want {
				t.Errorf("classifyFastPath(%v) = %v, want %v", tc.labels, got, tc.want)
			}
		})
	}
}

func TestClassifyFastPath_TitleMatch(t *testing.T) {
	cases := []struct {
		title string
		want  bool
		desc  string
	}{
		{"bump go-redis to v9.4.0", true, "bump dependency"},
		{"upgrade redis client", true, "upgrade dependency"},
		{"update dependency versions", true, "update dependency"},
		{"add tests for auth module", true, "add tests"},
		{"add test coverage for budget store", true, "add test coverage"},
		{"increase coverage for internal/mcp", true, "increase coverage"},
		{"fix typo in README", true, "fix typo"},
		{"fix comment in dispatch.go", true, "fix comment"},
		{"update readme with new API", true, "update readme"},
		{"add documentation for routing", true, "add documentation"},
		{"lint fix for internal packages", true, "lint fix"},
		{"format internal/sprint package", true, "format"},
		{"implement new feature X", false, "regular feature"},
		{"refactor dispatch layer", false, "refactor"},
		{"fix critical bug in auth", false, "bug fix (not titled as fast-path)"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			got := classifyFastPath(tc.title, nil)
			if got != tc.want {
				t.Errorf("classifyFastPath(%q, nil) = %v, want %v", tc.title, got, tc.want)
			}
		})
	}
}

func TestIsFastPath_DelegatesToField(t *testing.T) {
	fp := SprintItem{FastPath: true}
	if !IsFastPath(fp) {
		t.Error("expected IsFastPath=true for item with FastPath=true")
	}
	notFp := SprintItem{FastPath: false}
	if IsFastPath(notFp) {
		t.Error("expected IsFastPath=false for item with FastPath=false")
	}
}
