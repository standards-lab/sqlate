package query

import (
	"database/sql"
	"time"
)

// inUTC moves the time dest points to into time.UTC, for each destination
// kind database/sql scans a time into: a time.Time, a *time.Time, a valid
// sql.NullTime or sql.Null[time.Time], and an untyped destination holding a
// time.Time. Every other destination is left as it is. Scanner and Scalar
// run it on every value they scan, so their times do not depend on
// time.Local or on the driver: pgx, for one, delivers a timestamptz in
// time.Local, and the same row would read differently on two hosts. The
// instant is unchanged, and a zero time stays zero.
func inUTC(dest any) {
	switch d := dest.(type) {
	case *time.Time:
		*d = d.UTC()
	case **time.Time:
		if *d != nil {
			t := (*d).UTC()
			*d = &t
		}
	case *sql.NullTime:
		if d.Valid {
			d.Time = d.Time.UTC()
		}
	case *sql.Null[time.Time]:
		if d.Valid {
			d.V = d.V.UTC()
		}
	case *any:
		if t, ok := (*d).(time.Time); ok {
			*d = t.UTC()
		}
	}
}
