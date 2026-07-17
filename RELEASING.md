# Releasing

Go has no NuGet-style central package registry to publish to. A module becomes fetchable the
moment you push a semver git tag - that's the entire "publish" action.

## How Go module distribution actually works

1. **The repo itself is the package feed.** There's no separate build/upload/publish step. Once
   a commit is tagged `vX.Y.Z` and the tag is pushed, `go get github.com/daniellepelley/benzene-go@vX.Y.Z`
   works for anyone, immediately.
2. **`proxy.golang.org`** (the default `GOPROXY`) fetches directly from the repo the first time
   any `go` command asks for that version, then caches it forever - no action needed from us.
3. **`pkg.go.dev`** generates documentation automatically from exported identifiers' doc
   comments once the module has been fetched through the proxy at least once. Every package in
   this repo already has a package-level doc comment for exactly this reason.
4. **Versioning is semver via git tags**, nothing else. Pre-1.0 (`v0.x.y`) signals "the API can
   still change"; `v1.0.0` is the first commitment to backward compatibility.

## This repo's multi-module layout

This is a multi-module repo (see `go.work`):

| Module | Path | Why it's separate |
|---|---|---|
| `github.com/daniellepelley/benzene-go` | `.` (root) | The core library - zero third-party dependencies |
| `github.com/daniellepelley/benzene-go/awssqs` | `awssqs/` | Needs `aws-sdk-go-v2/service/sqs` - the *only* reason a package gets split out |
| `github.com/daniellepelley/benzene-go/awssns` | `awssns/` | Needs `aws-sdk-go-v2/service/sns` - same reason, same isolation pattern |
| `github.com/daniellepelley/benzene-go/awseventbridge` | `awseventbridge/` | Needs `aws-sdk-go-v2/service/eventbridge` - same isolation pattern |
| `github.com/daniellepelley/benzene-go/kafka` | `kafka/` | Needs `segmentio/kafka-go` (a broker wire protocol isn't hand-rollable) - same isolation pattern |
| `github.com/daniellepelley/benzene-go/diagnostics` | `diagnostics/` | Needs `go.opentelemetry.io/otel` (the OTel API - the SDK stays the application's) - same isolation pattern |
| `github.com/daniellepelley/benzene-go/examples/aws-sqs-helloworld` | `examples/aws-sqs-helloworld/` | Depends on *both* the root and `awssqs`; would be a dependency cycle inside either one |
| `github.com/daniellepelley/benzene-go/examples/aws-sns-helloworld` | `examples/aws-sns-helloworld/` | Depends on *both* the root and `awssns`; same reason |

**Policy**: a package gets its own module only when it has a genuine third-party dependency the
rest of the repo shouldn't carry (matching how OpenTelemetry-Go splits its exporters out from
its core). Everything else - including `awslambda` and `azurefunctions`, which are zero-dependency
today - stays in the root module. Don't split a package out speculatively; split it when a real
dependency actually shows up, exactly as happened with `awssqs` and `awssns`.

### Local development: `go.work`

`go.work` ties the modules together for local builds without needing their tags to be
resolvable over the network yet:

```
go 1.24.7

use (
	.
	./awssqs
	./awssns
	./examples/aws-sqs-helloworld
	./examples/aws-sns-helloworld
)

replace (
	github.com/daniellepelley/benzene-go v0.1.0 => ./
	github.com/daniellepelley/benzene-go/awssqs v0.1.0 => ./awssqs
	github.com/daniellepelley/benzene-go/awssns v0.1.0 => ./awssns
)
```

The `replace` lines are scoped to this workspace file only - unlike a `replace` inside a
module's own `go.mod` (which *would* affect real consumers), a `go.work` replace never leaks
out. They're needed because once a build touches a genuinely external dependency (the AWS SDK)
alongside a workspace-local one, Go's module graph reconciliation wants to read the workspace
module's real, tagged `go.mod` - which doesn't fully substitute for local resolution the way
`use` alone does. Once real tags exist and are network-resolvable, these lines become a
harmless no-op (same result, just skipping the network round-trip) - safe to leave in place.

## Cutting a release

```
git tag -a v0.1.0 -m "v0.1.0 - <one-line summary>"
git push origin v0.1.0
```

That's it - no build step, no upload, nothing else to run.

**Nested modules get their own tags**, prefixed with their subdirectory path (Go's documented
convention for multi-module repos):

```
git tag -a awssqs/v0.1.0 -m "awssqs v0.1.0"
git push origin awssqs/v0.1.0
```

The same applies to `awssns`:

```
git tag -a awssns/v0.1.0 -m "awssns v0.1.0"
git push origin awssns/v0.1.0
```

Only tag `examples/aws-sqs-helloworld` or `examples/aws-sns-helloworld` if you actually want one
independently `go get`-able, which nobody needs to do in practice - they're reference
deployments, not libraries; the `use`/`replace` entries in `go.work` are enough for this repo's
own CI and local development.

## Current status

A `v0.1.0` tag for the root module was created locally but **could not be pushed** in the
sandbox this was developed in - the session's git proxy rejected the tag push (403), unlike
ordinary branch pushes, which worked throughout. To actually publish the first release:

```
git push origin v0.1.0
```

Once that's live, `awssqs`'s and `awssns`'s own `require github.com/daniellepelley/benzene-go
v0.1.0` (and each example's requires on both its root and binding module) resolve for real over
the network too - though, as above, this repo's own tooling doesn't depend on that happening,
thanks to `go.work`.
