#!/usr/bin/env bash
# currency reports every dependency, toolchain, and pin that trails its latest
# release, one line each, and exits non-zero when it reports any. It reads the
# network and writes nothing. Run it through `mise run currency`. The actions it
# checks are those of the project root's workflows and, when the repository nests
# the project in a subdirectory, of the git top-level's. A scan command that fails
# fails currency.
set -euo pipefail
cd "$MISE_PROJECT_ROOT"
export GOWORK=off

stale=0
report() {
	echo "$1"
	stale=1
}

matches() {
	local pattern=$1 file
	local files=()
	shift
	for file in "$@"; do
		if [ -e "$file" ]; then files+=("$file"); fi
	done
	[ ${#files[@]} -gt 0 ] || return 0
	grep -ho "$pattern" "${files[@]}" || [ $? -eq 1 ]
}

report_outdated_requirements() {
	local mod=$1 updates line
	updates=$(cd "$mod" && go list -m -u -f \
		'{{if and (not .Main) (not .Indirect) .Update}}{{.Path}} {{.Version}} -> {{.Update.Version}}{{end}}' all)
	while read -r line; do
		if [ -n "$line" ]; then
			report "$mod/go.mod: $line"
		fi
	done <<<"$updates"
}

report_outdated_go_directive() {
	local mod=$1 go_minor=$2 directive
	directive=$(cd "$mod" && go mod edit -json | jq -r .Go | cut -d. -f1,2)
	[ "$directive" = "$go_minor" ] || report "$mod/go.mod: go $directive -> go $go_minor"
}

report_outdated_mise_tools() {
	local outdated line
	outdated=$(mise outdated --bump --local --json |
		jq -r 'to_entries[] | "\(.key) \(.value.requested) -> \(.value.bump)"')
	while read -r line; do
		if [ -n "$line" ]; then
			report "mise.toml: $line"
		fi
	done <<<"$outdated"
}

report_outdated_actions() {
	local actions uses action pin repo latest
	actions=$(matches 'uses: *[^ ]*@[^ ]*' "${workflows[@]}" | sed 's/uses: *//' | sort -u)
	for uses in $actions; do
		action=${uses%@*}
		pin=${uses#*@}
		repo=$(echo "$action" | cut -d/ -f1,2)
		latest=$(gh api "repos/$repo/releases/latest" --jq .tag_name)
		[ "$pin" = "$latest" ] || report ".github/workflows: $action $pin -> $latest"
	done
}

latest_dotted_release_tag() {
	local image=$1 pin=$2 suffix dotted_release_pattern latest
	suffix=${pin#"${pin%%[!0-9.]*}"}
	dotted_release_pattern="^[0-9]+(\.[0-9]+)+${suffix//./\\.}\$"
	latest=$(crane ls "$image" | grep -E "$dotted_release_pattern" | sed "s/${suffix}\$//" | sort -V | tail -n1) || return
	echo "$latest$suffix"
}

report_outdated_image_lines() {
	local images ref image pin latest
	images=$(matches '^ *image: *[^ ]*' compose.yml compose/*.yml "${workflows[@]}" |
		sed 's/^ *image: *//' | tr -d "\"'" | sort -u)
	for ref in $images; do
		image=${ref%:*}
		pin=${ref##*:}
		latest=$(latest_dotted_release_tag "$image" "$pin")
		[ "$pin" = "$latest" ] || report "images: $image $pin -> $latest"
	done
}

report_outdated_dockerfile_bases() {
	local dockerfile ref image pin latest
	for dockerfile in compose/*/Dockerfile; do
		[ -e "$dockerfile" ] || continue
		ref=$(sed -n '/^FROM /{s/^FROM  *\([^ ]*\).*/\1/p;q;}' "$dockerfile")
		image=${ref%:*}
		pin=${ref##*:}
		latest=$(latest_dotted_release_tag "$image" "$pin")
		[ "$pin" = "$latest" ] || report "$dockerfile: $image $pin -> $latest"
	done
}

root=$(pwd -P)
top=$(git rev-parse --show-toplevel 2>/dev/null || echo "$root")
workflows=("$root"/.github/workflows/*.yml)
[ "$top" = "$root" ] || workflows+=("$top"/.github/workflows/*.yml)

go_minor=$(mise latest go | cut -d. -f1,2)
for mod in $GO_MODULES; do
	report_outdated_requirements "$mod"
	report_outdated_go_directive "$mod" "$go_minor"
done
report_outdated_mise_tools
report_outdated_actions
report_outdated_image_lines
report_outdated_dockerfile_bases
exit "$stale"
