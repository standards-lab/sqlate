#!/usr/bin/env bash
# currency reports, or upgrades, every pin that trails its latest release: the mise tools, each Go
# module's go directive and direct requirements, the workflows' action pins, the image lines of
# the compose files and workflows, each compose Dockerfile's FROM tag, and each compose
# Dockerfile's `ARG <NAME>_VERSION` whose `ADD` fetches a GitHub repository at
# `v${<NAME>_VERSION}`, compared with that repository's highest semver tag. Both modes run the same
# scans. report prints one line for each trailing pin, a pin the HOLDS list names as held with its
# reason, and exits 1 when any it reports is not held; it reads the network and writes nothing.
# upgrade moves every trailing pin but a held one, printing one line for each. The actions it
# scans are those of the project root's workflows and, when the repository nests the project in a
# subdirectory, of the git top-level's. A scan command that fails fails currency.
# Run it through `mise run currency` or `mise run upgrade`.
#
# usage: scripts/currency.sh <report|upgrade>
# See architecture/principles/rolling-currency.md
set -euo pipefail
cd "$MISE_PROJECT_ROOT"
export GOWORK=off

if [ $# -ne 1 ] || { [ "$1" != report ] && [ "$1" != upgrade ]; }; then
	echo "usage: scripts/currency.sh <report|upgrade>" >&2
	exit 2
fi
action=$1
holds=${HOLDS:-}

held() {
	grep -m1 -E "^$(escape_regex "$1 $2: ")" <<<"$holds" | cut -d: -f2- | sed 's/^ //' || true
}

go() {
	mise exec -- go "$@"
}

escape_regex() {
	# shellcheck disable=SC2001 # a bracket expression, which a ${//} pattern has no escape for
	sed 's/[][\\.*^$|+?(){}]/\\&/g' <<<"$1"
}

existing() {
	local file
	for file in "$@"; do
		if [ -e "$file" ]; then echo "$file"; fi
	done
}

matches() {
	local pattern=$1
	shift
	if [ $# -eq 0 ]; then
		return 0
	fi
	grep -ho "$pattern" "$@" || [ $? -eq 1 ]
}

outdated_mise_tools() {
	mise outdated --bump --no-global --json |
		jq -r 'to_entries[] | select(.value.bump != null) | "\(.key) \(.value.requested) \(.value.bump)"'
}

outdated_requirements() {
	go list -m -u -f \
		'{{if and (not .Main) (not .Indirect) .Update}}{{.Path}} {{.Version}} {{.Update.Version}}{{end}}' all
}

trailing_go_directive() {
	local minor
	minor=$(go mod edit -json | jq -r .Go | cut -d. -f1,2) || return
	if [ "$minor" != "$go_minor" ]; then
		echo "go $minor $go_minor"
	fi
}

outdated_actions() {
	local actions uses name pinned repo latest
	actions=$(matches 'uses: *[^ ]*@[^ ]*' "${workflows[@]}" | sed 's/uses: *//' | sort -u) || return
	for uses in $actions; do
		name=${uses%@*}
		pinned=${uses#*@}
		repo=$(echo "$name" | cut -d/ -f1,2) || return
		latest=$(gh api "repos/$repo/releases/latest" --jq .tag_name) || return
		if [ "$pinned" != "$latest" ]; then
			echo "$name $pinned $latest"
		fi
	done
}

latest_dotted_release_tag() {
	local image=$1 pin=$2 suffix dotted_release_pattern tags latest
	suffix=${pin#"${pin%%[!0-9.]*}"}
	dotted_release_pattern="^[0-9]+(\.[0-9]+)+${suffix//./\\.}\$"
	tags=$(crane ls "$image") || return
	latest=$(grep -E "$dotted_release_pattern" <<<"$tags" | sed "s/${suffix}\$//" | sort -V | tail -n1) || return
	echo "$latest$suffix"
}

outdated_image_lines() {
	local images ref image pinned latest
	images=$(matches '^ *image: *[^ ]*' "${image_files[@]}" | sed 's/^ *image: *//' | tr -d "\"'" | sort -u) || return
	for ref in $images; do
		image=${ref%:*}
		pinned=${ref##*:}
		latest=$(latest_dotted_release_tag "$image" "$pinned") || return
		if [ "$pinned" != "$latest" ]; then
			echo "$image $pinned $latest"
		fi
	done
}

outdated_dockerfile_bases() {
	local dockerfile ref image pinned latest
	for dockerfile in compose/*/Dockerfile; do
		if [ ! -e "$dockerfile" ]; then
			continue
		fi
		ref=$(sed -n '/^FROM /{s/^FROM  *\([^ ]*\).*/\1/p;q;}' "$dockerfile") || return
		image=${ref%:*}
		pinned=${ref##*:}
		latest=$(latest_dotted_release_tag "$image" "$pinned") || return
		if [ "$pinned" != "$latest" ]; then
			echo "$image $pinned $latest $dockerfile"
		fi
	done
}

latest_semver_git_tag() {
	local repo=$1 tags latest
	tags=$(GIT_TERMINAL_PROMPT=0 git ls-remote --tags --refs "https://github.com/$repo.git") || return
	latest=$(sed -n 's|.*refs/tags/v\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)$|\1|p' <<<"$tags" | sort -V | tail -n1) || return
	if [ -z "$latest" ]; then
		return 1
	fi
	echo "$latest"
}

outdated_dockerfile_args() {
	local dockerfile args arg pinned repo latest
	for dockerfile in compose/*/Dockerfile; do
		if [ ! -e "$dockerfile" ]; then
			continue
		fi
		args=$(sed -n 's/^ARG  *\([A-Za-z0-9_]*_VERSION\)=\([^ ]*\) *$/\1 \2/p' "$dockerfile") || return
		while read -r arg pinned; do
			if [ -z "$arg" ]; then
				continue
			fi
			repo=$(sed -n "s|^ADD  *https://github\.com/\([^/ ]*/[^/ ]*\)\.git#v\\\${$arg} .*|\1|p" "$dockerfile") || return
			if [ -z "$repo" ]; then
				continue
			fi
			latest=$(latest_semver_git_tag "$repo") || return
			if [ "$pinned" != "$latest" ]; then
				echo "$repo $pinned $latest $dockerfile $arg"
			fi
		done <<<"$args"
	done
}

stale=0
trailing() {
	local reason
	reason=$(held "$2" "$3")
	if [ -n "$reason" ]; then
		echo "$1: $2 $3 -> $4 held: $reason"
	else
		echo "$1: $2 $3 -> $4"
		stale=1
	fi
}

report_mise_tools() {
	local scanned name pinned latest
	scanned=$(outdated_mise_tools) || return
	while read -r name pinned latest; do
		if [ -n "$name" ]; then trailing mise.toml "$name" "$pinned" "$latest"; fi
	done <<<"$scanned"
}

report_go_modules() {
	local mod scanned name pinned latest
	for mod in $GO_MODULES; do
		scanned=$(cd "$mod" && outdated_requirements && trailing_go_directive) || return
		while read -r name pinned latest; do
			if [ -n "$name" ]; then trailing "$mod/go.mod" "$name" "$pinned" "$latest"; fi
		done <<<"$scanned"
	done
}

report_actions() {
	local scanned name pinned latest
	scanned=$(outdated_actions) || return
	while read -r name pinned latest; do
		if [ -n "$name" ]; then trailing .github/workflows "$name" "$pinned" "$latest"; fi
	done <<<"$scanned"
}

report_image_lines() {
	local scanned name pinned latest
	scanned=$(outdated_image_lines) || return
	while read -r name pinned latest; do
		if [ -n "$name" ]; then trailing images "$name" "$pinned" "$latest"; fi
	done <<<"$scanned"
}

report_dockerfile_bases() {
	local scanned name pinned latest dockerfile
	scanned=$(outdated_dockerfile_bases) || return
	while read -r name pinned latest dockerfile; do
		if [ -n "$name" ]; then trailing "$dockerfile" "$name" "$pinned" "$latest"; fi
	done <<<"$scanned"
}

report_dockerfile_args() {
	local scanned name pinned latest dockerfile
	scanned=$(outdated_dockerfile_args) || return
	while read -r name pinned latest dockerfile _; do
		if [ -n "$name" ]; then trailing "$dockerfile" "$name" "$pinned" "$latest"; fi
	done <<<"$scanned"
}

upgrade_mise_tools() {
	local scanned name pinned latest
	local tools=()
	scanned=$(outdated_mise_tools) || return
	while read -r name pinned latest; do
		if [ -z "$name" ] || [ -n "$(held "$name" "$pinned")" ]; then
			continue
		fi
		echo "upgrade: mise.toml: $name $pinned -> $latest"
		tools+=("$name")
	done <<<"$scanned"
	if [ ${#tools[@]} -gt 0 ]; then
		mise upgrade --bump --no-global "${tools[@]}"
	fi
}

upgrade_go_module() {
	local mod=$1 scanned name pinned latest
	local requirements=()
	scanned=$(trailing_go_directive) || return
	if [ -n "$scanned" ]; then
		read -r name pinned latest <<<"$scanned"
		if [ -z "$(held go "$pinned")" ]; then
			echo "upgrade: $mod/go.mod: go $pinned -> $latest"
			go mod edit -go="$go_minor" -toolchain=none
		fi
	fi
	scanned=$(outdated_requirements) || return
	while read -r name pinned latest; do
		if [ -z "$name" ] || [ -n "$(held "$name" "$pinned")" ]; then
			continue
		fi
		echo "upgrade: $mod/go.mod: $name $pinned -> $latest"
		requirements+=("$name@latest")
	done <<<"$scanned"
	if [ ${#requirements[@]} -gt 0 ]; then
		go get "${requirements[@]}"
	fi
	go mod tidy
}

upgrade_go_modules() {
	local mod
	for mod in $GO_MODULES; do
		(
			cd "$mod"
			upgrade_go_module "$mod"
		)
	done
}

upgrade_actions() {
	local scanned name pinned latest key
	scanned=$(outdated_actions) || return
	while read -r name pinned latest; do
		if [ -z "$name" ] || [ -n "$(held "$name" "$pinned")" ]; then
			continue
		fi
		echo "upgrade: .github/workflows: $name $pinned -> $latest"
		key=$(escape_regex "$name@$pinned") || return
		sed -i -E "s#(uses: *)$key( |\$)#\\1$name@$latest\\2#" "${workflows[@]}"
	done <<<"$scanned"
}

upgrade_image_lines() {
	local scanned name pinned latest key
	scanned=$(outdated_image_lines) || return
	while read -r name pinned latest; do
		if [ -z "$name" ] || [ -n "$(held "$name" "$pinned")" ]; then
			continue
		fi
		echo "upgrade: images: $name $pinned -> $latest"
		key=$(escape_regex "$name:$pinned") || return
		sed -i -E "s#^( *image: *[\"']?)$key([\"']?( |\$))#\\1$name:$latest\\2#" "${image_files[@]}"
	done <<<"$scanned"
}

upgrade_dockerfile_bases() {
	local scanned name pinned latest dockerfile key
	scanned=$(outdated_dockerfile_bases) || return
	while read -r name pinned latest dockerfile; do
		if [ -z "$name" ] || [ -n "$(held "$name" "$pinned")" ]; then
			continue
		fi
		echo "upgrade: $dockerfile: $name $pinned -> $latest"
		key=$(escape_regex "$name:$pinned") || return
		sed -i -E "s#^(FROM +)$key( |\$)#\\1$name:$latest\\2#" "$dockerfile"
	done <<<"$scanned"
}

upgrade_dockerfile_args() {
	local scanned name pinned latest dockerfile arg key
	scanned=$(outdated_dockerfile_args) || return
	while read -r name pinned latest dockerfile arg; do
		if [ -z "$name" ] || [ -n "$(held "$name" "$pinned")" ]; then
			continue
		fi
		echo "upgrade: $dockerfile: $name $pinned -> $latest"
		key=$(escape_regex "$pinned") || return
		sed -i -E "s#^(ARG +$arg=)$key( *)\$#\\1$latest\\2#" "$dockerfile"
	done <<<"$scanned"
}

root=$(pwd -P)
top=$(git rev-parse --show-toplevel 2>/dev/null || echo "$root")
candidates=("$root"/.github/workflows/*.yml)
if [ "$top" != "$root" ]; then candidates+=("$top"/.github/workflows/*.yml); fi
mapfile -t workflows < <(existing "${candidates[@]}")
mapfile -t image_files < <(existing compose.yml compose/*.yml "${workflows[@]}")

go_minor=$(mise latest go | cut -d. -f1,2)
if [ "$action" = report ]; then
	report_mise_tools
	report_go_modules
	report_actions
	report_image_lines
	report_dockerfile_bases
	report_dockerfile_args
	exit "$stale"
fi
upgrade_mise_tools
upgrade_go_modules
upgrade_actions
upgrade_image_lines
upgrade_dockerfile_bases
upgrade_dockerfile_args
