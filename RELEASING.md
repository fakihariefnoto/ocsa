# Releasing

GoReleaser drives all three channels: GitHub releases, the Homebrew formula in
[fakihariefnoto/homebrew-tap](https://github.com/fakihariefnoto/homebrew-tap),
and `go install` via the module path.

```
git tag v0.1.0  ──▶  goreleaser  ──▶  GitHub release (archives + checksums)
                                  ──▶  Formula/ocsa.rb committed to the tap
                                  ──▶  go install github.com/fakihariefnoto/ocsa@v0.1.0
```

## Credentials

Publishing needs a credential that can create a release on `ocsa` and push to
`homebrew-tap`. **You probably already have one**: if `git push` works from this
machine, git stores a credential for github.com, and it can do both. No personal
access token has to exist.

The script resolves one in this order:

1. `TAP_GITHUB_TOKEN` in the environment
2. a token saved with `--store-token`, in the macOS keychain
3. **the credential git already stores for github.com** — the same one `git
   push` uses

Check what you have:

```sh
printf 'protocol=https\nhost=github.com\n\n' | git credential fill
```

If that prints a password, you are done. If it does not, either push something
once (git will offer to store a credential), or create a fine-grained PAT with
**Contents: read and write** on `fakihariefnoto/homebrew-tap` and pass it through
one of the first two steps.

### Locally

```sh
make release V=0.1.0 --dry-run       # print the plan and stop
make release V=0.1.0                 # tag, push, build, publish, update the tap
./scripts/release.sh --store-token   # only if you need to save a token by hand
```

The script refuses to run when it would produce a half-finished release:

| Check | Why |
|---|---|
| working tree is clean | an uncommitted change would not be in the release |
| on `main` | releases come from the trunk |
| not ahead of `origin/main` | the tag must point at pushed code |
| tag does not exist, locally or remotely | re-tagging is how versions get lost |
| token present | otherwise goreleaser fails after the tag is already pushed |

`TAP_GITHUB_TOKEN` in the environment overrides everything, which is useful in
CI or a one-off shell. The tap clone in `~/Projects/dist/...` is not used by the
release: goreleaser commits the formula through the API, so a stale local clone
cannot disagree with what gets published.

### In GitHub Actions, nothing on your machine

Add the PAT as a repository secret named `TAP_GITHUB_TOKEN`, then either push a
tag or run the **release** workflow manually with the tag as input. The workflow
runs the test suite first and refuses to publish if it fails.

### Is a secret safe in a public repository?

Yes. Repository secrets are not readable — by anyone, including you. Once saved
they are encrypted, shown only as a name, and cannot be read back through the UI
or the API. GitHub redacts them from logs, and workflows triggered by a fork's
pull request do not receive them at all.

The real trust boundary is not public visibility, it is **who can write to the
repository**: anyone able to merge a workflow file can add a step that prints a
secret. That is why the PAT should be fine-grained and scoped to the single tap
repository — if it leaked, the worst case is someone pushing to your tap, not
access to your account.

If you would rather hold no token in GitHub at all, use the local path; the
workflow is optional.

## What gets built

| | |
|---|---|
| targets | darwin and linux, amd64 and arm64 |
| archive | `tar.gz`, with `LICENSE` and `README.md` beside the binary |
| cgo | disabled, so one machine cross-compiles everything |
| version | injected with `-ldflags -X main.version/commit/date` |
| checksums | `checksums.txt` per release |

Windows is not built. The data-directory layout differs there and that path has
never been exercised, so it is not advertised rather than shipped untested.

Archives deliberately have no wrapping directory: the formula does
`bin.install "ocsa"` and expects the binary at the archive root, matching how
`commitly` is packaged in the same tap.

## A note on `goreleaser check`

It exits non-zero:

```
DEPRECATED: brews should not be used anymore
```

That is deliberate. `brews` was superseded by `homebrew_casks`, but casks are
macOS-only and the formula here also serves the Linux archives. The warning is
expected until the tap either splits into a cask and a formula or drops Linux.

## Verifying a release without publishing

```sh
make snapshot     # goreleaser release --snapshot --clean --skip=publish
```

Builds the archives and the formula into `dist/` and publishes nothing. This is
how the release configuration was checked before the first tag: four binaries,
four archives, a formula with both `on_macos` and `on_linux` blocks, and the
extracted binary reporting its injected version.

## If a release goes wrong

The tag is the problem, not the workflow. Delete it and start again:

```sh
git tag -d v0.1.0 && git push origin :refs/tags/v0.1.0
```

Then re-run. If the formula was already pushed to the tap, revert that commit in
the tap repository too, or the two will disagree about what `v0.1.0` is.

## Pre-release checklist

```sh
make check          # gofmt, go vet, tests
make snapshot       # confirm the artifacts build
git status          # clean
git log origin/main..HEAD   # nothing unpushed
```
