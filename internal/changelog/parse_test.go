package changelog

import (
	"errors"
	"testing"
)

// Real lines, as `juicefs changelog` 1.4.2 printed them for a scripted
// sequence on a test drive.
func TestParseRealLines(t *testing.T) {
	cases := []struct {
		line    string
		version int64
		op      string
		args    []string
		result  string
		session int64
		txn     int64
	}{
		{"6: 1795157315.178083457|CREATE(1,notes,1000,1000,2,493,18,,linux,true):2|(1,4)", 6, "CREATE",
			[]string{"1", "notes", "1000", "1000", "2", "493", "18", "", "linux", "true"}, "2", 1, 4},
		{"9: 1795157315.193339100|WRITE(3,0,0,1,6,1795157315,183781364):0|(1,7)", 9, "WRITE",
			[]string{"3", "0", "0", "1", "6", "1795157315", "183781364"}, "0", 1, 7},
		{"11: 1795157316.201082409|TRUNCATE(3,6,0,0)|(2,3)", 11, "TRUNCATE", []string{"3", "6", "0", "0"}, "", 2, 3},
		{"17: 1795157317.223413966|MOVE(2,.plan.md.tmp,2,plan.md,0,3,9223372032828243969):4|(1,12)", 17, "MOVE",
			[]string{"2", ".plan.md.tmp", "2", "plan.md", "0", "3", "9223372032828243969"}, "4", 1, 12},
		{"21: 1795157320.396590799|CLONE(4,2,plan.copy.md,6,0,18,true):6|(1,16)", 21, "CLONE",
			[]string{"4", "2", "plan.copy.md", "6", "0", "18", "true"}, "6", 1, 16},
		{"23: 1795157320.434028253|UNLINK(1,big.bin,9223372032828243969,false,false):5|(1,17)", 23, "UNLINK",
			[]string{"1", "big.bin", "9223372032828243969", "false", "false"}, "5", 1, 17},
		{"10: 1795157316.097462201|DIRSTAT(1,0,4096,1)|(1,8)", 10, "DIRSTAT", []string{"1", "0", "4096", "1"}, "", 1, 8},
	}
	for _, c := range cases {
		e, err := Parse(c.line)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if e.Version != c.version || e.Op != c.op || e.Result != c.result || e.Session != c.session || e.Txn != c.txn {
			t.Errorf("%q: got %+v", c.line, e)
		}
		if len(e.Args) != len(c.args) {
			t.Errorf("%q: args %q, want %q", c.line, e.Args, c.args)
			continue
		}
		for i := range c.args {
			if e.Args[i] != c.args[i] {
				t.Errorf("%q: arg %d = %q, want %q", c.line, i, e.Args[i], c.args[i])
			}
		}
	}
}

func TestNamesAreDecoded(t *testing.T) {
	// A name with a comma, a space, a percent sign and a letter with an accent.
	e, err := Parse("30: 1795157400.000000001|CREATE(1,a%2Cb c 100%25 caf%C3%A9,1000,1000,1,420,18,,linux,true):9|(2,5)")
	if err != nil {
		t.Fatal(err)
	}
	if e.Arg(1) != "a,b c 100% café" {
		t.Fatalf("name = %q", e.Arg(1))
	}
	if n, ok := e.ResultInt(); !ok || n != 9 {
		t.Fatalf("result %q", e.Result)
	}
}

func TestANameWithABarIsNotCut(t *testing.T) {
	// "|" is not escaped by JuiceFS.
	e, err := Parse("31: 1795157401.000000002|CREATE(1,left|right,1000,1000,1,420,18,,linux,true):10|(2,6)")
	if err != nil {
		t.Fatal(err)
	}
	if e.Op != "CREATE" || e.Arg(1) != "left|right" || e.Session != 2 {
		t.Fatalf("%+v", e)
	}
}

func TestEntriesWithoutASession(t *testing.T) {
	e, err := Parse("2: 1795157314.092813620|NEWSESSION(1,1795157374,{%22Version%22:%221.4.2%22%2C%22HostName%22:%22host%22})")
	if err != nil {
		t.Fatal(err)
	}
	if e.Op != "NEWSESSION" || e.Arg(0) != "1" || e.Session != 0 {
		t.Fatalf("%+v", e)
	}
}

func TestOtherLinesAreNotEntries(t *testing.T) {
	for _, l := range []string{"", "2026/11/20 12:18:25 juicefs[1] <INFO>: last version is 12", "no colon here", "x: y"} {
		if _, err := Parse(l); !errors.Is(err, ErrNotEntry) {
			t.Errorf("%q: want ErrNotEntry, got %v", l, err)
		}
	}
}
