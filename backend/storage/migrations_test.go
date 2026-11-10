package storage_test

import (
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/Herenn/Infralens/backend/migrations"
	"github.com/Herenn/Infralens/backend/storage/postgres/pgmigrations"
)

var migrationName = regexp.MustCompile(`^(\d{6})_([a-z0-9_]+)\.(up|down)\.sql$`)

// TestMigrationsContiguous: both migration directories hold the same
// versions, numbered 1..N without gaps or duplicates, each with an up and a
// down file under the same name. golang-migrate applies whatever it finds, so
// a gap (two features both picking 000004, a renumbering that left a hole)
// or a missing down file would otherwise only surface on someone's upgrade.
func TestMigrationsContiguous(t *testing.T) {
	dirs := map[string]fs.FS{"sqlite (backend/migrations)": migrations.FS, "postgres (pgmigrations)": pgmigrations.FS}
	names := map[string]map[int]string{}
	for label, fsys := range dirs {
		entries, err := fs.ReadDir(fsys, ".")
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		type pair struct{ name, up, down string }
		byVersion := map[int]*pair{}
		for _, e := range entries {
			m := migrationName.FindStringSubmatch(e.Name())
			if m == nil {
				t.Errorf("%s: unexpected file %q", label, e.Name())
				continue
			}
			v, _ := strconv.Atoi(m[1])
			p := byVersion[v]
			if p == nil {
				p = &pair{name: m[2]}
				byVersion[v] = p
			}
			if p.name != m[2] {
				t.Errorf("%s: version %d has two names: %q and %q", label, v, p.name, m[2])
			}
			if m[3] == "up" {
				p.up = e.Name()
			} else {
				p.down = e.Name()
			}
		}
		versions := make([]int, 0, len(byVersion))
		for v := range byVersion {
			versions = append(versions, v)
		}
		sort.Ints(versions)
		names[label] = map[int]string{}
		for i, v := range versions {
			if v != i+1 {
				t.Errorf("%s: versions not contiguous from 1: got %v", label, versions)
				break
			}
		}
		for _, v := range versions {
			p := byVersion[v]
			if p.up == "" || p.down == "" {
				t.Errorf("%s: version %d (%s) needs both up and down files (up=%q down=%q)", label, v, p.name, p.up, p.down)
			}
			names[label][v] = p.name
		}
		if len(versions) < 6 {
			t.Errorf("%s: %d migrations, want at least 6 (v3.1 adds 000004-000006)", label, len(versions))
		}
	}
	sq, pg := names["sqlite (backend/migrations)"], names["postgres (pgmigrations)"]
	if len(sq) != len(pg) {
		t.Errorf("sqlite has %d migrations, postgres %d", len(sq), len(pg))
	}
	for v, n := range sq {
		if pg[v] != n {
			t.Errorf("version %d: sqlite %q, postgres %q - the two stores must migrate in step", v, n, pg[v])
		}
	}
}
