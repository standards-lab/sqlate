package migrate

import "time"

func SetAfter(m *Migrator, after func(time.Duration) <-chan time.Time) { m.after = after }
