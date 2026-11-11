package storage

import (
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// CheckSchemaNotNewer refuses a database whose schema is newer than the
// newest migration embedded in fsys - one already migrated by a newer
// InfraLens. Downgrades are not supported, in either DB_AUTO_MIGRATE mode:
// the newer schema may have changed what this code reads and writes, and
// golang-migrate cannot migrate up from a version it has no file for.
func CheckSchemaNotNewer(fsys fs.FS, version int, ok bool) error {
	latest, _, err := embeddedMigrations(fsys)
	if err != nil {
		return err
	}
	if ok && version > latest {
		return fmt.Errorf("database schema is at version %d, newer than this backend knows (%d): it was migrated by a newer InfraLens. "+
			"Downgrades are not supported - run that newer version again, or restore a backup taken before the upgrade", version, latest)
	}
	return nil
}

// CheckSchema compares the schema version a store's database records
// (golang-migrate's schema_migrations: version, dirty, ok=false when there is
// none) with the newest migration embedded in fsys, and reports a database
// that is behind as an error naming the missing migrations, and one that is
// ahead as CheckSchemaNotNewer does.
//
// Used when DB_AUTO_MIGRATE=false: a backend running against an older schema
// half-works - new features fail with "no such table" on every request or
// background pass - so refusing to start with a clear message is better.
func CheckSchema(fsys fs.FS, version int, dirty, ok bool) error {
	latest, names, err := embeddedMigrations(fsys)
	if err != nil {
		return err
	}
	if err := CheckSchemaNotNewer(fsys, version, ok); err != nil {
		return err
	}
	if ok && dirty {
		return fmt.Errorf("database schema is marked dirty at version %d (an interrupted migration): fix it, then run the migrations or set DB_AUTO_MIGRATE=true", version)
	}
	if !ok {
		version = 0
	}
	if version >= latest {
		return nil
	}
	var missing []string
	for _, v := range sortedKeys(names) {
		if v > version {
			missing = append(missing, names[v])
		}
	}
	return fmt.Errorf("database schema is at version %d but this backend needs %d; missing migrations: %s. "+
		"Run them, or set DB_AUTO_MIGRATE=true", version, latest, strings.Join(missing, ", "))
}

// LatestMigration is the newest migration version embedded in fsys.
func LatestMigration(fsys fs.FS) (int, error) {
	latest, _, err := embeddedMigrations(fsys)
	return latest, err
}

func embeddedMigrations(fsys fs.FS) (int, map[int]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return 0, nil, fmt.Errorf("reading embedded migrations: %w", err)
	}
	names := map[int]string{}
	latest := 0
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".up.sql") {
			continue
		}
		i := strings.Index(n, "_")
		if i <= 0 {
			continue
		}
		v, err := strconv.Atoi(n[:i])
		if err != nil {
			continue
		}
		names[v] = strings.TrimSuffix(n, ".up.sql")
		if v > latest {
			latest = v
		}
	}
	return latest, names, nil
}

func sortedKeys(m map[int]string) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// NodeIntervalsMigration is the migration that creates node_intervals, and
// nodeIntervalColumns the columns it creates. Migration 7 was amended before
// v3.1.0 was released (group_name was added), so a database migrated by an
// unreleased development build records version 7 with a different table.
const NodeIntervalsMigration = 7

var nodeIntervalColumns = []string{"id", "node_name", "group_name", "first_seen", "last_seen"}

// NodeIntervalsLookupIndex is the index heartbeats are extended through,
// and nodeIntervalLookupColumns its columns in order. A release candidate
// built it on (node_name, last_seen).
const NodeIntervalsLookupIndex = "idx_node_intervals_lookup"

var nodeIntervalLookupColumns = []string{"group_name", "node_name", "last_seen"}

// CheckNodeIntervals verifies node_intervals has the columns and lookup
// index this backend uses, given what the database reports (columns empty:
// no such table; lookup: the index's columns in order, empty when missing)
// and its schema version. Below NodeIntervalsMigration there is nothing to
// check.
func CheckNodeIntervals(version int, columns, lookup []string) error {
	if version < NodeIntervalsMigration {
		return nil
	}
	have := map[string]bool{}
	for _, c := range columns {
		have[strings.ToLower(c)] = true
	}
	var missing []string
	for _, c := range nodeIntervalColumns {
		if !have[c] {
			missing = append(missing, c)
		}
	}
	var what string
	switch {
	case len(columns) == 0:
		what = "is missing"
	case len(missing) > 0:
		what = "lacks column(s) " + strings.Join(missing, ", ")
	case strings.ToLower(strings.Join(lookup, ",")) != strings.Join(nodeIntervalLookupColumns, ","):
		got := "missing"
		if len(lookup) > 0 {
			got = "on (" + strings.Join(lookup, ", ") + ")"
		}
		what = fmt.Sprintf("has its lookup index %s %s instead of on (%s)", NodeIntervalsLookupIndex, got, strings.Join(nodeIntervalLookupColumns, ", "))
	default:
		return nil
	}
	return fmt.Errorf("table node_intervals %s although the schema is at version %d: this database was created by an unreleased "+
		"development build of InfraLens v3.1, whose migration %d differed from the released one. To recover, delete the database "+
		"if it only holds demo or test data; otherwise drop the table and set the migration version back so migration %d runs "+
		"again (agent heartbeat history starts over): DROP TABLE IF EXISTS node_intervals; UPDATE schema_migrations SET version = %d, dirty = false;",
		what, version, NodeIntervalsMigration, NodeIntervalsMigration, NodeIntervalsMigration-1)
}
