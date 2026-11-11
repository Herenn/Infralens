package storage

import (
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// CheckSchema compares the schema version a store's database records
// (golang-migrate's schema_migrations: version, dirty, ok=false when there is
// none) with the newest migration embedded in fsys, and reports a database
// that is behind as an error naming the missing migrations.
//
// Used when DB_AUTO_MIGRATE=false: a backend running against an older schema
// half-works - new features fail with "no such table" on every request or
// background pass - so refusing to start with a clear message is better.
// A database *newer* than the binary (a rollback) is allowed: newer
// migrations only add tables and columns this code does not use.
func CheckSchema(fsys fs.FS, version int, dirty, ok bool) error {
	latest, names, err := embeddedMigrations(fsys)
	if err != nil {
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
