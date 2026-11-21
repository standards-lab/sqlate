package query

import (
	"database/sql"
	"time"
)

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
