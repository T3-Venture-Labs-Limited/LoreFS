package jfs

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleMountinfo = `25 1 0:23 / / rw,relatime shared:1 - btrfs /dev/mapper/root rw,ssd
101 25 0:60 / /home/alice/Lore rw,nosuid,nodev,relatime shared:55 - fuse.juicefs JuiceFS:lorefs rw,user_id=1000,group_id=1000
102 25 0:61 / /home/alice/My\040Drive rw,nosuid,nodev shared:56 - fuse.juicefs JuiceFS:x rw,user_id=1000
103 25 0:62 / /home/alice/Team-Shared rw,nosuid,nodev shared:57 - fuse.rclone team: rw,user_id=1000
`

func TestMountedIn(t *testing.T) {
	f := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(f, []byte(sampleMountinfo), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"/home/alice/Lore":        true,
		"/home/alice/Lore/":       true,
		"/home/alice/My Drive":    true,  // escaped space in the mount table
		"/home/alice/Team-Shared": false, // mounted, but not by JuiceFS
		"/home/alice/Lore/notes":  false, // inside a mount is not a mount point
		"/home/alice/Elsewhere":   false,
	}
	for folder, want := range cases {
		if got := mountedIn(f, folder); got != want {
			t.Errorf("mountedIn(%q) = %v, want %v", folder, got, want)
		}
	}
	if mountedIn(filepath.Join(t.TempDir(), "missing"), "/home/alice/Lore") {
		t.Error("a missing mount table means not mounted")
	}
}

func TestCopyClonerFileAndFolder(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src", "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "a.txt"), []byte("A"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "sub", "b.txt"), []byte("B"), 0o600)
	if err := (Copy{}).Clone(filepath.Join(dir, "src", "a.txt"), filepath.Join(dir, "a-copy.txt")); err != nil {
		t.Fatal(err)
	}
	if err := (Copy{}).Clone(filepath.Join(dir, "src"), filepath.Join(dir, "dst")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"a-copy.txt": "A", "dst/a.txt": "A", "dst/sub/b.txt": "B"} {
		got, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", path, got, err)
		}
	}
	// Cloning onto an existing file must fail rather than overwrite.
	if err := (Copy{}).Clone(filepath.Join(dir, "src", "a.txt"), filepath.Join(dir, "a-copy.txt")); err == nil {
		t.Error("expected an error when the destination exists")
	}
}
