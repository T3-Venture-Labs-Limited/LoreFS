// Package changelog reads JuiceFS's metadata changelog: the list of
// operations (create, write, rename, delete, ...) that the drive's clients
// have made, each stamped with the client session that made it.
//
// JuiceFS prints one entry per line:
//
//	VERSION: SECONDS.NANOS|OPERATION(arg,arg,...)[:result]|(SESSION,TRANSACTION)
//
// Names inside arguments are written with %XX escapes for control
// characters, commas, percent signs, parentheses, quotes, backslashes and
// every byte outside printable ASCII. A "|" is not escaped, so the line is
// cut at the first and the last "|" only.
package changelog

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Entry is one operation of the changelog.
type Entry struct {
	Version int64     // the cursor to resume from
	Time    time.Time // when the client made the change
	Op      string    // CREATE, WRITE, MOVE, ...
	Args    []string  // the operation's arguments, names decoded
	Result  string    // text after the closing parenthesis, often an inode
	Session int64     // the JuiceFS client session that made the change
	Txn     int64     // transaction number within that session
}

// ErrNotEntry is returned for a line that is not a changelog entry (a log
// message printed by the program, an empty line).
var ErrNotEntry = errors.New("not a changelog entry")

// Parse reads one line.
func Parse(line string) (Entry, error) {
	var e Entry
	line = strings.TrimRight(line, "\r\n")
	colon := strings.Index(line, ": ")
	if colon <= 0 {
		return e, ErrNotEntry
	}
	v, err := strconv.ParseInt(strings.TrimSpace(line[:colon]), 10, 64)
	if err != nil {
		return e, ErrNotEntry
	}
	e.Version = v
	rest := line[colon+2:]

	first := strings.IndexByte(rest, '|')
	last := strings.LastIndexByte(rest, '|')
	if first < 0 {
		return e, fmt.Errorf("changelog entry %d is cut off: %w", v, ErrNotEntry)
	}
	ts := rest[:first]
	sec, nano, _ := strings.Cut(ts, ".")
	s, err1 := strconv.ParseInt(sec, 10, 64)
	n, err2 := strconv.ParseInt(nano, 10, 64)
	if err1 != nil || err2 != nil {
		return e, fmt.Errorf("changelog entry %d has a bad time %q", v, ts)
	}
	e.Time = time.Unix(s, n)

	// The session comes last: "|(session,transaction)". Entries without one
	// (a session starting) have a single "|".
	op := rest[first+1:]
	if last > first {
		if who := rest[last+1:]; strings.HasPrefix(who, "(") && strings.HasSuffix(who, ")") {
			if sid, txn, ok := strings.Cut(who[1:len(who)-1], ","); ok {
				e.Session, _ = strconv.ParseInt(sid, 10, 64)
				e.Txn, _ = strconv.ParseInt(txn, 10, 64)
				op = rest[first+1 : last]
			}
		}
	}

	open := strings.IndexByte(op, '(')
	closeAt := strings.LastIndexByte(op, ')')
	if open <= 0 || closeAt < open {
		return e, fmt.Errorf("changelog entry %d has no argument list", v)
	}
	e.Op = op[:open]
	if tail := op[closeAt+1:]; strings.HasPrefix(tail, ":") {
		e.Result = tail[1:]
	}
	inner := op[open+1 : closeAt]
	if inner != "" {
		for _, a := range strings.Split(inner, ",") {
			e.Args = append(e.Args, decode(a))
		}
	}
	return e, nil
}

// decode undoes the %XX escapes.
func decode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(n))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Arg returns argument i, or "" when there is none.
func (e Entry) Arg(i int) string {
	if i < 0 || i >= len(e.Args) {
		return ""
	}
	return e.Args[i]
}

// Int returns argument i as a number, and whether it was one.
func (e Entry) Int(i int) (int64, bool) {
	n, err := strconv.ParseInt(e.Arg(i), 10, 64)
	return n, err == nil
}

// ResultInt returns the result as a number, and whether it was one.
func (e Entry) ResultInt() (int64, bool) {
	n, err := strconv.ParseInt(e.Result, 10, 64)
	return n, err == nil
}
