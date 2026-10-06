# Contributing

Thank you for your interest in contributing! This document covers how to submit changes.

## Workflow

1. **Fork** the repository
2. **Create a branch** from `main`:
   ```bash
   git checkout -b feat/my-feature
   ```
3. **Make your changes** and commit following the guidelines below
4. **Push** to your fork and open a **Pull Request** against `main`

## Branch Naming

| Prefix | Use case |
|--------|----------|
| `feat/` | New feature or behavior change |
| `fix/` | Bug fix |
| `docs/` | Documentation only |
| `build/` | Dependencies, Dockerfile, GoReleaser |
| `ci/` | Workflows, Renovate |

## Commit Guidelines

We follow [Conventional Commits](https://www.conventionalcommits.org/), one line per commit; the details go in the pull request:

```
<type>(<scope>): <description>
```

**Types:** `feat`, `fix`, `docs`, `build`, `ci`, `refactor`, `test`, `perf`

Pull requests are squash-merged and the **PR title becomes the commit**, so the title must follow this format; a check enforces it. The commits inside the pull request can be anything.

**Examples:**
```
feat(flux): follow kustomization objects from clusters/
fix(probe): treat null without a default as redundant
docs(readme): update quick look output
build(deps): update helm sdk to v4.3.1
```

## Development

The Go version is in `go.mod`. Before opening a pull request:

```bash
gofmt -l .
go vet ./...
go test ./...
```

- Changed a command or flag? Run `go run ./internal/tools/gendocs` to regenerate `docs/cli`; CI fails if it is out of date.
- Changed an output? Run `go test ./internal/cli -run Golden -update` and review the golden files.
- Changed a script in `scripts/`? Run `shellcheck` on it.
- CI fails if coverage drops below 80% in total or 90% in `internal/probe` (see `.testcoverage.yml`).

No Go installed? The same commands run in the `golang` image:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.27 go test ./...
```

## Scope

Contributions are welcome for:

- Bug fixes, ideally with a chart or values in `testdata/` that reproduces them
- Charts or templates where a key is reported wrongly
- GitOps sources not supported yet (see the end of [DESIGN.md](DESIGN.md))
- Documentation

If unsure whether your idea fits, open an issue first to discuss it.

## Guidelines

- **No secrets** — never commit credentials, tokens or keys, not even in `testdata/`
- **Keep PRs focused** — one feature or fix per PR
- **Update docs** — if your change affects how deadvalues is used, update the README or `docs/`
