module github.com/standards-lab/sqlate/sqlint

go 1.27

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/standards-lab/sqlate v0.1.0
)

// Transient bridge while this module builds against the base module's
// unreleased v0.2.0 changes; the v0.2.0 release drops this.
replace github.com/standards-lab/sqlate => ../
