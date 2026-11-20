package jfs

import (
	"strings"
	"testing"
	"time"
)

const cannedStatus = `{
  "Setting": {
    "Name": "team",
    "UUID": "a1540637-fef6-4ce8-8b14-ccb6d123a96d",
    "Storage": "s3",
    "Bucket": "https://store.example.net/bucket",
    "TrashDays": 1,
    "ChangeLog": true,
    "ChangeLogMaxAge": 86400,
    "ChangeLogMaxLines": 100000
  },
  "Sessions": [
    {"Sid": 1, "Expire": "2026-11-20T17:04:05+05:30", "Version": "1.4.2+2026-10-22.46af7af",
     "HostName": "laptop", "IPAddrs": ["192.0.2.1"], "MountPoint": "/home/alice/Lore",
     "MountTime": "2026-11-20T17:03:05.835109779+05:30", "ProcessID": 160525},
    {"Sid": 2, "Expire": "2026-11-20T17:04:06+05:30", "Version": "1.4.2",
     "HostName": "desktop", "MountPoint": "/home/bob/Lore", "MountTime": "2026-11-20T16:00:00Z", "ProcessID": 7}
  ],
  "Stat": {"UsedSpace": 4096, "AvailableSpace": 1125899906842624, "UsedInodes": 12, "AvailableInodes": 10485760}
}`

func TestParseStatus(t *testing.T) {
	s, err := ParseStatus([]byte(cannedStatus))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "team" || s.Storage != "s3" || !s.ChangeLog || s.ChangeLogMaxAge != 24*time.Hour || s.ChangeLogMaxLines != 100000 {
		t.Fatalf("%+v", s)
	}
	if len(s.Sessions) != 2 || s.Sessions[0].Host != "laptop" || s.Sessions[1].MountPoint != "/home/bob/Lore" {
		t.Fatalf("%+v", s.Sessions)
	}
	if o := s.OtherSessions("laptop", "/home/alice/Lore"); len(o) != 1 || o[0].Host != "desktop" {
		t.Fatalf("other sessions: %+v", o)
	}
}

func TestParseStatusSkipsLeadingLogLines(t *testing.T) {
	s, err := ParseStatus([]byte("2026/11/20 17:02:57 <INFO>: Meta address: x\n" + cannedStatus))
	if err != nil || s.Name != "team" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestParseStatusChangeLogOff(t *testing.T) {
	s, err := ParseStatus([]byte(strings.Replace(cannedStatus, `"ChangeLog": true`, `"ChangeLog": false`, 1)))
	if err != nil || s.ChangeLog {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestParseStatusRefusesAnEmptyIndex(t *testing.T) {
	if _, err := ParseStatus([]byte(`{"Setting": {}, "Sessions": []}`)); err == nil {
		t.Fatal("an index with no drive should be an error")
	}
	if _, err := ParseStatus([]byte("garbage")); err == nil {
		t.Fatal("garbage should be an error")
	}
}
