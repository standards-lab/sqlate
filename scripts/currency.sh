#!/usr/bin/env bash
# currency reports every dependency, toolchain, and pin that trails its latest
# release, one line each, and exits non-zero when it reports any. It reads the
# network and writes nothing. Run it through `mise run currency`.
set -euo pipefail
cd "$MISE_PROJECT_ROOT"
export GOWORK=off

stale=0
report() {
	echo "$1"
	stale=1
}

go_minor=$(mise latest go | cut -d. -f1,2)

for mod in $GO_MODULES; do
	# Direct requirements with a newer version within their major.
	while read -r line; do
		[ -n "$line" ] && report "$mod/go.mod: $line"
	done < <(cd "$mod" && go list -m -u -f \
		'{{if and (not .Main) (not .Indirect) .Update}}{{.Path}} {{.Version}} -> {{.Update.Version}}{{end}}' all)

	# The go directive's minor against the current Go minor.
	directive=$(cd "$mod" && go mod edit -json | jq -r .Go | cut -d. -f1,2)
	[ "$directive" = "$go_minor" ] || report "$mod/go.mod: go $directive -> go $go_minor"
done

# mise tools pinned below their latest version.
while read -r line; do
	[ -n "$line" ] && report "mise.toml: $line"
done < <(mise outdated --bump --local --json |
	jq -r 'to_entries[] | "\(.key) \(.value.requested) -> \(.value.bump)"')

# GitHub Actions pinned below the action's latest release.
for uses in $(grep -ho 'uses: *[^ ]*@[^ ]*' .github/workflows/*.yml | sed 's/uses: *//' | sort -u); do
	action=${uses%@*}
	pin=${uses#*@}
	repo=$(echo "$action" | cut -d/ -f1,2)
	latest=$(gh api "repos/$repo/releases/latest" --jq .tag_name)
	[ "$pin" = "$latest" ] || report ".github/workflows: $action $pin -> $latest"
done

# Compose and CI service images pinned below the highest semver tag that
# carries the pinned tag's variant suffix (18.6-alpine -> -alpine).
for ref in $(grep -ho '^ *image: *[^ ]*' compose.yml compose/*.yml .github/workflows/*.yml 2>/dev/null |
	sed 's/^ *image: *//' | tr -d "\"'" | sort -u); do
	image=${ref%:*}
	pin=${ref##*:}
	suffix=${pin#"${pin%%[!0-9.]*}"}
	pattern="^[0-9]+(\.[0-9]+)*${suffix//./\\.}\$"
	latest=$(crane ls "$image" | grep -E "$pattern" | sed "s/${suffix}\$//" | sort -V | tail -n1)
	latest="$latest$suffix"
	[ "$pin" = "$latest" ] || report "images: $image $pin -> $latest"
done

exit "$stale"
