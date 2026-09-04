package sqlint

import "strconv"

// stubDialect is the dialect statement directories compile against: $N
// placeholders, no error mapping. Compilation needs a dialect only for
// the placeholders, and the lint runs no SQL.
type stubDialect struct{}

func (stubDialect) Name() string { return "sqlint" }

func (stubDialect) Placeholder(n int) string { return "$" + strconv.Itoa(n) }

func (stubDialect) MapError(err error) error { return err }
