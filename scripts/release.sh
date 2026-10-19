#!/usr/bin/env bash
#
# Release ocsa, updating the Homebrew tap in the same pass.
#
# This is the local path: the token lives in your environment or your macOS
# keychain and is never uploaded anywhere. If you would rather not hold a token
# on this machine at all, use the GitHub Actions workflow instead
# (.github/workflows/release.yml) and set TAP_GITHUB_TOKEN as a repository
# secret there.
#
# Usage:
#   ./scripts/release.sh                   # patch bump from the last tag
#   ./scripts/release.sh minor             # or major|minor|patch
#   ./scripts/release.sh 0.1.0             # an explicit version
#   ./scripts/release.sh --dry-run         # show what would happen
#   ./scripts/release.sh --store-token     # optional: save a token explicitly
#
# The version comes from the highest v* tag, not from a file, so the tag is the
# single source of truth and cannot drift from what was published.
#
# The token is only needed to publish: the GitHub release, and the formula
# commit to fakihariefnoto/homebrew-tap. If you have pushed to GitHub from this
# machine before, git already stores a credential that can do both, and this
# script uses it. No personal access token has to exist.
#
# Resolution order:
#   1. TAP_GITHUB_TOKEN in the environment
#   2. a token saved with --store-token, in the macOS keychain
#   3. the credential git already stores for github.com
#
# Any of them must be able to push to the tap repository. For (3) that is the
# same credential `git push` uses, so if `git push origin main` works, this
# works.

set -euo pipefail

TOKEN_SERVICE="ocsa-tap-token"
TOKEN_ACCOUNT="${USER:-$(id -un)}"
REPO_SLUG="fakihariefnoto/ocsa"
TAP_REPO="fakihariefnoto/homebrew-tap"

DRY_RUN=0
STORE_TOKEN=0
VERSION=""

die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
say() { printf '\033[36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$*" >&2; }

usage() {
	cat <<'EOF'
Release ocsa, updating the Homebrew tap in the same pass.

This is the local path: the token lives in your environment or your macOS
keychain and is never uploaded anywhere. If you would rather not hold a token
on this machine at all, use the GitHub Actions workflow instead
(.github/workflows/release.yml) and set TAP_GITHUB_TOKEN as a repository
secret there.

Usage:
  ./scripts/release.sh                   # patch bump from the last tag
  ./scripts/release.sh minor             # major | minor | patch
  ./scripts/release.sh 0.1.0             # an explicit version
  ./scripts/release.sh --dry-run         # print the plan and stop
  ./scripts/release.sh --store-token     # save a token in the keychain

With no version and no bump, the next patch version is derived from the highest
v* tag. With no tag at all, the first release is 0.1.0.

Credentials, in order: TAP_GITHUB_TOKEN, the keychain, then the credential git
already stores for github.com. The last one means no personal access token has
to exist if `git push` works here.
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--store-token) STORE_TOKEN=1 ;;
	--dry-run) DRY_RUN=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	-*) die "unknown flag $1" ;;
	*) VERSION="${1#v}" ;;
	esac
	shift
done

# --- token handling ---------------------------------------------------------

store_token() {
	command -v security >/dev/null 2>&1 ||
		die "no macOS keychain here; export TAP_GITHUB_TOKEN instead"
	say "Storing the tap token in the keychain as \"$TOKEN_SERVICE\""
	printf 'Token (input hidden, then press enter): '
	# -U replaces an existing item. The value is read from the prompt rather
	# than argv, so it never appears in the process table.
	security add-generic-password -a "$TOKEN_ACCOUNT" -s "$TOKEN_SERVICE" -U -w
	say "Stored. Verify with: security find-generic-password -s $TOKEN_SERVICE >/dev/null && echo ok"
}

# git_credential asks git for the credential it already stores for github.com.
# This is the one `git push` uses, so a machine that can push can release.
git_credential() {
	command -v git >/dev/null 2>&1 || return 1
	printf 'protocol=https\nhost=github.com\n\n' |
		git credential fill 2>/dev/null |
		sed -n 's/^password=//p'
}

# token_source reports where a usable token would come from, or fails.
token_source() {
	if [ -n "${TAP_GITHUB_TOKEN:-}" ]; then
		printf 'environment'
		return 0
	fi
	if command -v security >/dev/null 2>&1 &&
		security find-generic-password -s "$TOKEN_SERVICE" >/dev/null 2>&1; then
		printf 'keychain'
		return 0
	fi
	if [ -n "$(git_credential)" ]; then
		printf 'git credential'
		return 0
	fi
	return 1
}

load_token() {
	if [ -n "${TAP_GITHUB_TOKEN:-}" ]; then
		export TAP_GITHUB_TOKEN
		return 0
	fi
	if command -v security >/dev/null 2>&1 &&
		security find-generic-password -s "$TOKEN_SERVICE" >/dev/null 2>&1; then
		TAP_GITHUB_TOKEN="$(security find-generic-password -a "$TOKEN_ACCOUNT" -s "$TOKEN_SERVICE" -w)"
		export TAP_GITHUB_TOKEN
		return 0
	fi
	TAP_GITHUB_TOKEN="$(git_credential)"
	export TAP_GITHUB_TOKEN
	# goreleaser wants a token for the release upload too; the same credential
	# is used for both, which is why one variable is enough.
	export GITHUB_TOKEN="$TAP_GITHUB_TOKEN"
}

if [ "$STORE_TOKEN" = "1" ]; then
	store_token
	exit 0
fi

# latest_tag is the highest v* tag by version order, not by creation date: a
# backported patch would otherwise become the base for the next release. It
# reads the remote as well as the local repository, because a release can be
# published from another machine.
latest_tag() {
	{
		git tag -l 'v[0-9]*.[0-9]*.[0-9]*'
		git ls-remote --tags --refs origin 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null |
			sed 's|.*refs/tags/||'
	} | sort -V | tail -1
}

# bump takes the last tag and returns the next version. With no tag the first
# release is 0.1.0: 1.0.0 would promise an API that does not exist yet.
bump() {
	local base="${1:-v0.0.0}" kind="$2"
	local major minor patch
	IFS=. read -r major minor patch <<<"${base#v}"
	major="${major:-0}"
	minor="${minor:-0}"
	patch="${patch:-0}"
	case "$kind" in
	major)
		major=$((major + 1))
		minor=0
		patch=0
		;;
	minor)
		minor=$((minor + 1))
		patch=0
		;;
	patch) patch=$((patch + 1)) ;;
	*) die "unknown bump \"$kind\"; use major, minor or patch" ;;
	esac
	printf '%s.%s.%s' "$major" "$minor" "$patch"
}

LAST_TAG="$(latest_tag)"
REQUESTED="$VERSION"
case "$REQUESTED" in
"")
	# Default: a patch bump, which is the common case for a fix.
	if [ -n "$LAST_TAG" ]; then
		VERSION="$(bump "$LAST_TAG" patch)"
		DERIVED="patch bump from $LAST_TAG"
	else
		VERSION="0.1.0"
		DERIVED="first release"
	fi
	;;
major | minor | patch)
	VERSION="$(bump "${LAST_TAG:-v0.0.0}" "$VERSION")"
	[ -n "$LAST_TAG" ] || VERSION="0.1.0"
	DERIVED="$REQUESTED bump from ${LAST_TAG:-no tag}"
	;;
*)
	[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
		die "version must look like 1.2.3, or be major|minor|patch (got \"$VERSION\")"
	DERIVED="explicit"
	;;
esac
TAG="v$VERSION"

if [ -n "$LAST_TAG" ] && [ "$(printf '%s\n%s\n' "${LAST_TAG#v}" "$VERSION" | sort -V | tail -1)" != "$VERSION" ]; then
	die "$VERSION is not newer than the last release $LAST_TAG"
fi

# --- preconditions ----------------------------------------------------------

say "Checking the working tree"
[ -z "$(git status --porcelain)" ] ||
	die "uncommitted changes; commit or stash them first"

BRANCH="$(git rev-parse --abbrev-ref HEAD)"
[ "$BRANCH" = "main" ] || die "on branch \"$BRANCH\"; releases come from main"

REMOTE="$(git remote get-url origin 2>/dev/null || true)"
[ -n "$REMOTE" ] || die "no origin remote"

# Compare against the remote as it is now: a stale origin/main would let an
# unpushed commit through.
say "Fetching origin"
git fetch --quiet --tags origin main 2>/dev/null ||
	warn "could not reach origin; comparing against the local origin/main"

[ -z "$(git log origin/main..HEAD 2>/dev/null)" ] ||
	die "local main is ahead of origin/main; push first"

if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
	die "tag $TAG already exists locally"
fi

if git ls-remote --exit-code --tags origin "refs/tags/$TAG" >/dev/null 2>&1; then
	die "tag $TAG already exists on the remote"
fi

command -v goreleaser >/dev/null 2>&1 || die "goreleaser is not installed"

SOURCE=""
if ! SOURCE="$(token_source)"; then
	die "no credential for GitHub: run ./scripts/release.sh --store-token, export TAP_GITHUB_TOKEN, or push something with git first"
fi
say "GitHub credential found in the $SOURCE"

# --- plan -------------------------------------------------------------------

cat <<EOF

  version   $VERSION  (tag $TAG, $DERIVED)
  branch    $BRANCH
  remote    $REMOTE
  tap       $TAP_REPO  (via $SOURCE)
  targets   darwin+linux, amd64+arm64

  This will:
    1. tag $TAG and push it
    2. run goreleaser release --clean, which builds the archives,
       creates the GitHub release on $REPO_SLUG, and commits
       Formula/ocsa.rb to $TAP_REPO

EOF

if [ "$DRY_RUN" = "1" ]; then
	say "Dry run: nothing was tagged, pushed or built."
	exit 0
fi

# --- go ---------------------------------------------------------------------

say "Running the test suite"
go test ./...

say "Tagging $TAG"
git tag -a "$TAG" -m "ocsa $VERSION"

say "Pushing $TAG"
git push origin "$TAG"

say "Releasing"
load_token
goreleaser release --clean

say "Done."
cat <<EOF

  release   https://github.com/$REPO_SLUG/releases/tag/$TAG
  install   brew tap fakihariefnoto/tap && brew install fakihariefnoto/tap/ocsa

EOF
