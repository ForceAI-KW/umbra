package runner

import "testing"

func TestValidRepo(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"x/y", true},
		{"ForceAI-KW/force-website-builder", true},
		{"org.name/repo_name-1.2", true},
		{`x"; rm -rf /;/y`, false},
		{"x/y\"; rm -rf /", false},
		{"x/`whoami`", false},
		{"x/$(whoami)", false},
		{"no-slash", false},
		{"too/many/slashes", false},
		{"", false},
		{"/y", false},
		{"x/", false},
	}
	for _, c := range cases {
		if got := ValidRepo(c.in); got != c.want {
			t.Errorf("ValidRepo(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestValidRunnerField(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"a,b,c", true},
		{"wsl2,umbra-ci", true},
		{"fwb-ci5-fwb-1", true},
		{"actions-runner-fwb-1", true},
		{`x"; rm -rf /`, false},
		{"x`whoami`", false},
		{"x$(whoami)", false},
		{"has space", false},
		{"", false},
	}
	for _, c := range cases {
		if got := ValidRunnerField(c.in); got != c.want {
			t.Errorf("ValidRunnerField(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
