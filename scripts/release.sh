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
#   ./scripts/release.sh --store-token     # save the tap token in the keychain
#   ./scripts/release.sh 0.1.0             # tag, push, build, publish
#   ./scripts/release.sh 0.1.0 --dry-run   # show what would happen
#
# The token is a fine-grained PAT with "Contents: read and write" on
# fakihariefnoto/homebrew-tap, and nothing else.

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
  ./scripts/release.sh --store-token     # save the tap token in the keychain
  ./scripts/release.sh 0.1.0             # tag, push, build, publish
  ./scripts/release.sh 0.1.0 --dry-run   # show what would happen

The token is a fine-grained PAT with "Contents: read and write" on
fakihariefnoto/homebrew-tap, and nothing else.
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

# token_from returns the token from the environment, else the keychain.
token_from() {
	if [ -n "${TAP_GITHUB_TOKEN:-}" ]; then
		printf 'environment'
		return 0
	fi
	if command -v security >/dev/null 2>&1 &&
		security find-generic-password -s "$TOKEN_SERVICE" >/dev/null 2>&1; then
		printf 'keychain'
		return 0
	fi
	return 1
}

load_token() {
	if [ -n "${TAP_GITHUB_TOKEN:-}" ]; then
		export TAP_GITHUB_TOKEN
		return 0
	fi
	TAP_GITHUB_TOKEN="$(security find-generic-password -a "$TOKEN_ACCOUNT" -s "$TOKEN_SERVICE" -w)"
	export TAP_GITHUB_TOKEN
}

if [ "$STORE_TOKEN" = "1" ]; then
	store_token
	exit 0
fi

[ -n "$VERSION" ] || {
	usage
	die "give a version, e.g. ./scripts/release.sh 0.1.0"
}
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
	die "version must look like 1.2.3 (got \"$VERSION\")"
TAG="v$VERSION"

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
git fetch --quiet origin main 2>/dev/null ||
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
if ! SOURCE="$(token_from)"; then
	die "no tap token: run ./scripts/release.sh --store-token, or export TAP_GITHUB_TOKEN"
fi
say "Tap token found in the $SOURCE"

# --- plan -------------------------------------------------------------------

cat <<EOF

  version   $VERSION  (tag $TAG)
  branch    $BRANCH
  remote    $REMOTE
  tap       $TAP_REPO  (via $SOURCE token)
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
