package migrate_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/standards-lab/sqlate/migrate"
)

func TestFiles_ParsesTheLayoutAndTheTransactionHeader(t *testing.T) {
	fsys := fstest.MapFS{
		"m/0001_first.up.sql":   {Data: []byte("CREATE TABLE a (x int)")},
		"m/0001_first.down.sql": {Data: []byte("DROP TABLE a")},
		"m/0003_third.up.sql":   {Data: []byte("--| transaction: none\n-- built outside a transaction\n\nCREATE INDEX CONCURRENTLY ix ON a (x)")},
		"m/0003_third.down.sql": {Data: []byte("--| transaction: none\nDROP INDEX CONCURRENTLY ix")},
		"m/0002_second.up.sql":  {Data: []byte("-- a comment that is not a declaration\nALTER TABLE a ADD y int")},
		"m/notes.txt":           {Data: []byte("ignored? no: every file must match")},
	}
	if _, err := migrate.Files(fsys, "m"); err == nil || !strings.Contains(err.Error(), "notes.txt") {
		t.Fatalf("a stray file was accepted: %v", err)
	}
	delete(fsys, "m/notes.txt")

	ms, err := migrate.Files(fsys, "m")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(ms) != 3 || ms[0].Version != 1 || ms[1].Version != 2 || ms[2].Version != 3 {
		t.Fatalf("versions = %+v", ms)
	}
	if ms[0].Name != "first" || !ms[0].Transactional || ms[0].Down != "DROP TABLE a" {
		t.Errorf("first = %+v", ms[0])
	}
	if !ms[1].Transactional || ms[1].Down != "" {
		t.Errorf("second = %+v", ms[1])
	}
	if ms[2].Transactional || ms[2].Up != "CREATE INDEX CONCURRENTLY ix ON a (x)" || ms[2].Down != "DROP INDEX CONCURRENTLY ix" {
		t.Errorf("third = %+v (header must opt out; the engine receives the body only)", ms[2])
	}
}

func TestFiles_RejectsBrokenSets(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"down without up":  {"m/0001_a.down.sql": {Data: []byte("x")}},
		"bad name":         {"m/1_a.sql": {Data: []byte("x")}},
		"two names":        {"m/0001_a.up.sql": {Data: []byte("x")}, "m/0001_b.down.sql": {Data: []byte("x")}},
		"headers disagree": {"m/0001_a.up.sql": {Data: []byte("--| transaction: none\nx")}, "m/0001_a.down.sql": {Data: []byte("x")}},
		"zero version":     {"m/0000_a.up.sql": {Data: []byte("x")}},
		"bad transaction":  {"m/0001_a.up.sql": {Data: []byte("--| transaction: maybe\nx")}},
		"malformed header": {"m/0001_a.up.sql": {Data: []byte("--| transaction none\nx")}},
	}
	for name, fsys := range cases {
		if _, err := migrate.Files(fsys, "m"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
