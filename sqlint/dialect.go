package sqlint

import "strconv"

type stubDialect struct{}

func (stubDialect) Name() string { return "sqlint" }

func (stubDialect) Placeholder(n int) string { return "$" + strconv.Itoa(n) }

func (stubDialect) MapError(err error) error { return err }
