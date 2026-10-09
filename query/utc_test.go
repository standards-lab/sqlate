package query_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/standards-lab/sqlate/query"
	"github.com/standards-lab/sqlate/sqltest"
)

// stamped holds every time destination kind Scanner converts, one of them
// reached through an embedded struct.
type stamped struct {
	Identity
	Updated  *time.Time          `json:"updated"`
	Deleted  *time.Time          `json:"deleted"`
	Seen     sql.NullTime        `json:"seen"`
	Archived sql.Null[time.Time] `json:"archived"`
	Never    time.Time           `json:"never"`
	Raw      any                 `json:"raw"`
	Missing  sql.Null[time.Time] `json:"missing"`
}

func TestScanner_ReturnsTimesInUTC(t *testing.T) {
	// A winter instant: London's wall clock (time.Local, from TestMain)
	// reads as UTC's, offset zero, so only a comparison of whole values
	// tells a value left in time.Local from one in time.UTC.
	at := time.Date(2026, 1, 15, 12, 30, 0, 500, time.Local)
	utc := time.Date(2026, 1, 15, 12, 30, 0, 500, time.UTC)
	// A summer instant, an hour ahead of UTC on London's wall clock.
	summer := time.Date(2026, 7, 1, 9, 0, 0, 0, time.Local)
	summerUTC := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	db, _ := session(t, sqltest.Response{
		Columns: []string{"id", "created_at", "updated", "deleted", "seen", "archived", "never", "raw", "missing"},
		Rows:    [][]driver.Value{{"a", at, summer, nil, at, summer, time.Time{}, at, nil}},
	})
	got, err := source(t).Statement("all").Scan(query.Scanner[stamped]()).One(context.Background(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.CreatedAt != utc {
		t.Errorf("embedded created_at = %v (%v), want %v", got.CreatedAt, got.CreatedAt.Location(), utc)
	}
	if got.Updated == nil || *got.Updated != summerUTC {
		t.Errorf("updated = %v, want %v", got.Updated, summerUTC)
	}
	if got.Deleted != nil {
		t.Errorf("deleted = %v, want nil for NULL", got.Deleted)
	}
	if got.Seen != (sql.NullTime{Time: utc, Valid: true}) {
		t.Errorf("seen = %+v, want %v", got.Seen, utc)
	}
	if got.Archived != (sql.Null[time.Time]{V: summerUTC, Valid: true}) {
		t.Errorf("archived = %+v, want %v", got.Archived, summerUTC)
	}
	if got.Never != (time.Time{}) {
		t.Errorf("never = %v, want the zero time", got.Never)
	}
	if raw, ok := got.Raw.(time.Time); !ok || raw != utc {
		t.Errorf("raw = %#v, want %v", got.Raw, utc)
	}
	if got.Missing != (sql.Null[time.Time]{}) {
		t.Errorf("missing = %+v, want invalid and zero", got.Missing)
	}
}

func TestScalar_ReturnsTimesInUTC(t *testing.T) {
	at := time.Date(2026, 7, 1, 9, 0, 0, 0, time.Local)
	utc := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	one := func() sqltest.Response {
		return sqltest.Response{Columns: []string{"at"}, Rows: [][]driver.Value{{at}}}
	}
	ctx := context.Background()
	stmt := source(t).Statement("all")

	db, _ := session(t, one())
	if got, err := stmt.Scan(query.Scalar[time.Time]).One(ctx, db, nil); err != nil || got != utc {
		t.Errorf("Scalar[time.Time] = %v (%v), %v; want %v", got, got.Location(), err, utc)
	}
	db, _ = session(t, one())
	if got, err := stmt.Scan(query.Scalar[*time.Time]).One(ctx, db, nil); err != nil || got == nil || *got != utc {
		t.Errorf("Scalar[*time.Time] = %v, %v; want %v", got, err, utc)
	}
	db, _ = session(t, one())
	if got, err := stmt.Scan(query.Scalar[sql.NullTime]).One(ctx, db, nil); err != nil || got != (sql.NullTime{Time: utc, Valid: true}) {
		t.Errorf("Scalar[sql.NullTime] = %+v, %v; want %v", got, err, utc)
	}
	db, _ = session(t, sqltest.Response{Columns: []string{"at"}, Rows: [][]driver.Value{{time.Time{}}}})
	if got, err := stmt.Scan(query.Scalar[time.Time]).One(ctx, db, nil); err != nil || got != (time.Time{}) {
		t.Errorf("Scalar of the zero time = %v, %v; want the zero time", got, err)
	}
}

func TestContinue_CarriesATimeKeyInUTC(t *testing.T) {
	// The page's last item is keyed by a summer instant London's wall clock
	// reads an hour ahead of UTC; the cursor carries it as UTC text, so the
	// same row issues the same cursor on every host.
	last := time.Date(2026, 7, 1, 9, 0, 0, 0, time.Local)
	db, _ := session(t, sqltest.WithTotal(members(last.Add(-time.Hour), last, last.Add(time.Hour)), 9))
	page, err := memberView(t).List(context.Background(), db, query.Directives{}, query.Page{Number: 1, Size: 2})
	if err != nil || page.Next == "" {
		t.Fatalf("List = %+v, %v; want a cursor", page, err)
	}
	db, rec := session(t, sqltest.WithTotal(members(), 9))
	if _, err := memberView(t).Continue(context.Background(), db, query.Directives{}, page.Next, 2); err != nil {
		t.Fatal(err)
	}
	if got := rec.Calls()[0].Args[1]; got != "2026-07-01T08:00:00Z" {
		t.Errorf("keyed time bound as %v, want its UTC text", got)
	}
}
