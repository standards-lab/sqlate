package migrate

import "time"

// SetAfter replaces m's lock-wait timer, so a test drives the TryLock
// backoff without sleeping.
func SetAfter(m *Migrator, after func(time.Duration) <-chan time.Time) { m.after = after }
