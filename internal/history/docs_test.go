package history

import (
	"os"
	"strings"
	"testing"
)

// The documentation lists the built-in skip patterns. This keeps the two
// from drifting apart.
func TestDocumentedSkipListMatchesDefaults(t *testing.T) {
	data, err := os.ReadFile("../../docs/version-history.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	section := doc[strings.Index(doc, "## What is not recorded"):strings.Index(doc, "### Changing the list")]
	blocks := strings.Split(section, "```")
	if len(blocks) < 7 {
		t.Fatalf("expected three code blocks in the section, found %d parts", len(blocks))
	}
	lines := func(block string) []string {
		var out []string
		for _, l := range strings.Split(block, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	check := func(what string, documented, defaults []string) {
		if strings.Join(documented, "\n") != strings.Join(defaults, "\n") {
			t.Errorf("%s in docs/version-history.md differ from the defaults in skip.go:\n  docs:     %q\n  defaults: %q", what, documented, defaults)
		}
	}
	check("folder patterns", lines(blocks[1]), DefaultSkipDirs)
	check("file patterns", lines(blocks[3]), DefaultSkipFiles)
	check("video patterns", lines(blocks[5]), DefaultSkipMedia)
}
