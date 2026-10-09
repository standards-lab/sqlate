package query

import (
	"database/sql"
	"time"
)

// Every time a scan the library provides returns is in time.UTC, whatever
// time zone the process runs in and whatever location the driver gave the
// value: a driver such as pgx delivers a timestamptz in time.Local, so the
// same row would read differently on two hosts. The instant is unchanged;
// only its location is. A zero time stays zero, since its UTC form is
// itself.

// inUTC moves the time dest points to into UTC, for each destination kind
// database/sql scans a time into: a time.Time, a *time.Time, a valid
// sql.NullTime or sql.Null[time.Time], and an untyped destination holding a
// time.Time. Every other destination is left as it is.
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
