package assets

import (
	"strings"
	"testing"
)

func TestRulesTemplateIsNeutralAndVersioned(t *testing.T) {
	if got := RulesVersionOf(AgentsRules); got != RulesVersion {
		t.Fatalf("the template carries version %d, the program says %d", got, RulesVersion)
	}
	if !strings.HasSuffix(strings.TrimSpace(AgentsRules), "-->") {
		t.Error("the version must be a trailing comment")
	}
	for _, word := range []string{"company", "Inc.", "Ltd"} {
		if strings.Contains(strings.ToLower(AgentsRules), strings.ToLower(word)) {
			t.Errorf("the template mentions %q", word)
		}
	}
}

func TestRulesVersionOf(t *testing.T) {
	cases := map[string]int{
		"text\n<!-- lorefs-rules-version: 3 -->\n":                                    3,
		"edited by hand, no version":                                                  0,
		"<!-- lorefs-rules-version: x -->":                                            0,
		"<!-- lorefs-rules-version: 2":                                                0,
		"a\n<!-- lorefs-rules-version: 1 -->\nmore\n<!-- lorefs-rules-version: 4 -->": 4,
	}
	for in, want := range cases {
		if got := RulesVersionOf(in); got != want {
			t.Errorf("%q: %d, want %d", in, got, want)
		}
	}
}
