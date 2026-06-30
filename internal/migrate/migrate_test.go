package migrate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverOrdersAndChecksums(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0002_second.sql", "select 2;")
	write(t, dir, "0001_first.sql", "select 1;")
	write(t, dir, "notes.txt", "ignore me")            // non-.sql ignored
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	migs, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) != 2 {
		t.Fatalf("expected 2 migrations, got %d", len(migs))
	}
	if migs[0].Version != "0001_first" || migs[1].Version != "0002_second" {
		t.Fatalf("wrong order: %s, %s", migs[0].Version, migs[1].Version)
	}
	if migs[0].Checksum == "" || migs[0].Checksum == migs[1].Checksum {
		t.Fatalf("checksums missing or not unique: %q %q", migs[0].Checksum, migs[1].Checksum)
	}
}

func TestDiscoverChecksumIsStableAndContentSensitive(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0001_x.sql", "select 1;")
	a, _ := Discover(dir)

	// Same content → same checksum.
	b, _ := Discover(dir)
	if a[0].Checksum != b[0].Checksum {
		t.Fatal("checksum should be stable for identical content")
	}

	// Changed content → different checksum (this is what catches drift).
	write(t, dir, "0001_x.sql", "select 2;")
	c, _ := Discover(dir)
	if a[0].Checksum == c[0].Checksum {
		t.Fatal("checksum should change when content changes")
	}
}

func TestDiscoverMissingDir(t *testing.T) {
	if _, err := Discover(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected an error for a missing directory")
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
