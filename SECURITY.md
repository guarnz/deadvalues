# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability, please **do not open a public issue**. Report it privately through GitHub Security Advisories:

**https://github.com/guarnz/deadvalues/security/advisories/new**

Include a description of the issue and steps to reproduce. You can expect an acknowledgment within 48 hours and an initial assessment within 7 days. Once the issue is confirmed and a fix is available, the advisory will be published.

## What deadvalues touches

- **No cluster access** — charts are rendered client-side with the Helm SDK, like `helm template`; nothing connects to Kubernetes, and `lookup` returns nothing
- **Network** — only to download charts from the HTTP and OCI repositories your manifests or flags point to
- **Credentials** — read from Helm's `repositories.yaml` and registry config, used only to download charts, never written or printed
- **Files** — writes only the chart cache (`--no-cache` turns it off), the reports you ask for with `--output-file`, and, with `prune`, the values files it reports, after showing a diff and asking (unless `--yes`)
- **Encrypted values** — SOPS-encrypted Secrets are skipped, not decrypted

## Values in reports

Reports show the **values** of the keys they list, so a secret written in plain text in a values file can appear in the terminal, in a report file, or in a pull request comment posted by the GitHub Action. Keep secrets out of values files (use a secret manager or SOPS). Keys listed under `ignore` in `.deadvalues.yaml` are left out of the table and Markdown reports, but the JSON report still includes them.

## Supply Chain

- **Signed releases** — binaries ship with checksums signed with cosign (keyless) and an SBOM
- **Signed image** — `ghcr.io/guarnz/deadvalues` is signed with cosign
- **GitHub Action** — downloads the release binary and verifies it against `checksums.txt`; the `args` input is split, never evaluated by the shell
- **GitHub Actions pinned by digest** — Renovate pins actions to a commit SHA (`pinDigests`) so a moved tag cannot alter a workflow
- **Renovate Bot** — keeps Go modules, actions and the base image up to date
- **OpenSSF Scorecard** — runs on the repository and publishes its score
- **govulncheck** — fails the build when the code calls a known vulnerable function, the Go standard library included; also runs weekly
- **CodeQL** — static analysis of the Go code on every push and pull request
- **Fuzzing** — the values parser is fuzz-tested, so malformed YAML cannot crash it

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| main | Yes |
