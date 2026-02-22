# RDS IAM Proxy

Local MySQL proxy for GUI clients (`MySQL Workbench`, `Sequel Ace`, `mysql` CLI) that authenticates to AWS RDS using IAM DB tokens.

## What It Does

1. Listens for local MySQL client connections on `127.0.0.1:<port>`
2. Validates local proxy credentials (`proxy_user` / `proxy_password`)
3. Generates (and caches) RDS IAM auth token
4. Connects to RDS over TLS
5. Forwards MySQL traffic between client and RDS

## Key Features

- MySQL server-side handshake for GUI compatibility
- IAM token generation with cache/refresh
- TLS-only backend connection to RDS
- Profile-based config (single or multi-profile startup)
- Connection pool prewarm (single-use backend conns)
- Graceful shutdown with in-flight drain
- Profile-level `max_conns` with hard cap
- Dry-run mode for IAM verification

## Requirements

- Go `1.25+`
- AWS credentials available (env, profile, SSO, role, etc.)
- IAM permission: `rds-db:connect`
- DB user configured for IAM DB auth
- AWS RDS CA bundle (e.g. `global-bundle.pem`)

## Recommended Clients

Any MySQL-compatible client should work in theory, including IDE database extensions. If you hit issues with a specific client, please report it.

- Sequel Ace (macOS only): <https://sequel-ace.com/>
- MySQL Workbench: <https://dev.mysql.com/downloads/workbench/>

## Downloads

Pre-built binaries for Linux, macOS, and Windows (amd64 and arm64):

**[→ GitHub Releases](https://github.com/n-i-m-a/rds-iam-proxy/releases)**

Each release includes per-platform `.tar.gz` archives, and `checksums.txt` for verification.

## Quick Start

1. Copy config:
   - `cp config.example.yaml config.yaml`
2. Set strong passwords and real profile values
3. Download the AWS RDS global certificate bundle and point `ca_bundle` to it:
   - <https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem>
4. Start proxy:

```bash
go run ./cmd/rds-iam-proxy --profile prod-reporting
```

## Configuration

Config search order:

1. `--config <path>`
2. `config.yaml` in current directory, then one parent directory up
3. `config.yaml` in executable directory, then one parent directory up
4. `~/.config/rds-iam-proxy/config.yaml`

Startup logs include the selected config path/source. On lookup failures, logs include all checked paths.

Sample (`config.example.yaml`) defines `profiles`.

### Profile Fields

- `name`: unique profile name
- `listen_addr`: must be loopback (`127.0.0.1:<port>`)
- `max_conns`: max concurrent client conns for this profile (default `20`, hard max `200`)
- `proxy_user`: local client username
- `proxy_password`: local client password
- `rds_host`: RDS endpoint host
- `rds_port`: optional, default `3306`
- `rds_region`: AWS region (e.g. `eu-west-1`)
- `rds_db_user`: IAM DB username used against RDS
- `aws_profile`: optional AWS shared config profile
- `default_db`: optional default DB for backend session
- `ca_bundle`: path to CA PEM file

Relative paths (including `ca_bundle`) are resolved from the directory containing the selected `config.yaml`, not from the binary location or shell working directory.

### Validation Rules

- Non-loopback `listen_addr` is rejected
- Empty/default `proxy_password` is rejected (unless explicitly allowed for dev)
- `proxy_user` and `rds_db_user` must be different (per profile)
- If multiple profiles exist:
  - all `proxy_user` values must be unique
- Selected profiles cannot reuse the same `listen_addr`

## Run Modes

### Single profile

```bash
go run ./cmd/rds-iam-proxy --profile prod-reporting
```

### Multiple profiles in one process

```bash
go run ./cmd/rds-iam-proxy --profiles prod-reporting,staging-app
```

### All profiles

```bash
go run ./cmd/rds-iam-proxy --all-profiles
```

### Interactive selection

If multiple profiles exist and no profile flags are passed, startup menu asks:
- run one profile
- run multiple profiles
- run all profiles

## Dry Run

Validate IAM token generation without starting listeners:

```bash
go run ./cmd/rds-iam-proxy --profile prod-reporting --dry-run
```

Output includes masked token metadata and expiry.

## CLI Flags

- `--config <path>`
- `--profile <name>`
- `--profiles <name1,name2,...>`
- `--all-profiles`
- `--verbose` (enables verbose structured logs; default output is compact)
- `--dry-run`
- `--pool-size <n>`
- `--max-conns <n>` (override profile value; still capped at `200`)
- `--log-level debug|info|warn|error`
- `--shutdown-timeout 30s`
- `--connect-timeout 8s`
- `--allow-dev-empty-password` (dev only)

## Scripts

- `bash scripts/test.sh`
  - `gofmt` + `go test ./...`
- `bash scripts/run.sh --profile <name>`
  - starts proxy with passthrough flags
- `bash scripts/build-all.sh`
  - cross-builds binaries to `dist/`:
    - `linux/amd64`, `linux/arm64`
    - `darwin/amd64`, `darwin/arm64`
    - `windows/amd64`, `windows/arm64`
- `bash scripts/clean.sh`
  - removes build output (`dist/` by default)
- `bash scripts/release.sh v1.0.0`
  - tags and pushes; CI creates the GitHub Release and uploads assets
- `bash scripts/test-real-local.sh`
  - local-only real environment test (never used by CI)
  - reads local env file `scripts/test-real-local.env` (gitignored)
  - runs IAM dry-run by default
  - optional full query-through-proxy mode
  - full mode auto-resolves host/port/user/password from the selected profile

## CI / GitHub Actions

- `.github/workflows/ci.yml`
  - runs on push/PR
  - executes `bash scripts/test.sh`
  - executes `go test -race ./...`
  - runs local-only end-to-end proxy flow test (included in `go test ./...`)
  - executes cross-build matrix (`bash scripts/build-all.sh`)
  - uploads `dist/` artifacts

- `.github/workflows/release.yml`
  - runs on tags matching `v*`
  - builds cross-platform binaries
  - generates checksums
  - publishes release assets to GitHub Releases

## Logging

Structured `slog` text logs, including:

- startup listener info (profile, listen addr, backend host, max conns)
- connection lifecycle (`conn_id`, `remote_addr`, duration)
- bytes transferred (`bytes_up`, `bytes_down`)
- auth/backend/pool warnings and errors

Default logs are compact and include timestamp (level is hidden for readability).
Use `--verbose` to enable full structured logs (timestamp, level, and source), and `--log-level` to control verbosity threshold.

## Security Notes

- Proxy intentionally binds only loopback addresses
- Do not commit real `config.yaml` or cert material
- Use unique users per profile to reduce blast radius
- Start with conservative `max_conns` (`10-20`) for team use

## Testing Levels

- Unit tests:
  - config validation/defaults
  - token cache behavior
  - pool lifecycle and refill behavior
  - pipe and connection-close handling
- Local-only end-to-end test:
  - starts a fake local MySQL backend
  - starts `rds-iam-proxy` against that backend
  - runs a real MySQL query through the proxy (`SELECT 1`)
  - no AWS/RDS access required
- Local real-environment smoke test (non-CI):
  - configure `scripts/test-real-local.env` (from `.example.env`)
  - uses your real local config/credentials on developer machine
  - default mode: `--dry-run` only
  - optional mode: starts proxy and executes real SQL query through it

### Local Real Test Setup (non-CI)

1. Copy env template:
   - `cp scripts/test-real-local.example.env scripts/test-real-local.env`
2. Set at least:
   - `PROFILE=<your profile>`
3. Run:
   - `bash scripts/test-real-local.sh`
4. Optional full query mode:
   - set `RUN_FULL_PROXY_TEST=1`
   - proxy connection values are auto-resolved from selected profile
   - only set `PROXY_HOST/PROXY_PORT/PROXY_USER/PROXY_PASSWORD` if you need to override
   - run script again

## Common Errors

- `x509: certificate signed by unknown authority`
  - Wrong/expired CA bundle; use current RDS global bundle
- `ERROR 1045 Access denied`
  - Wrong `rds_db_user`, missing IAM permission, or DB user not IAM-enabled
- `Malformed communication packet`
  - Usually protocol capability mismatch; ensure latest proxy build is running

## Downloaded Binary Warnings

When downloading binaries from GitHub Releases, users may see OS trust warnings if binaries are not code-signed.

### What users may see

- macOS: "cannot be opened because the developer cannot be verified"
- Windows: SmartScreen "unrecognized app" warning
- Linux: usually no OS popup, but some endpoint/security tools may warn

### Workarounds (until signed releases are added)

- Prefer checksum verification before first run:
  - compare downloaded file against release `checksums.txt`

- macOS:
  - remove quarantine attribute (recommended for CLI binaries):
    - `xattr -d com.apple.quarantine "/absolute/path/to/rds-iam-proxy"`
  - or allow via System Settings (after one blocked launch attempt):
    - try to run the binary once from Terminal so macOS records the block
    - open System Settings > Privacy & Security
    - under the blocked app/security warning, click `Allow Anyway`
    - run the binary again, then click `Open` in the confirmation dialog
  - optional Finder route (may be inconsistent for plain CLI binaries):
    - right-click `rds-iam-proxy` and choose `Open`
    - confirm the prompt to allow first run

- Windows:
  - in SmartScreen, click "More info" then "Run anyway" (only after checksum verification)

### Project Status and Responsibility

This is a personal project shared as-is. I do not monetize it, and at this stage I do not maintain paid software-signing and notarization infrastructure for release binaries.

The source, build scripts, and release process are intentionally kept transparent so you can review and verify what you run.

Use this software at your own risk. I do not assume responsibility for misuse, abuse, service disruption, data loss, or any direct/indirect damage resulting from its use.

## Release Assets

GitHub Releases include:

- platform folders in `dist/rds-iam-proxy_<os>_<arch>/` where the executable name stays:
  - `rds-iam-proxy` (Linux/macOS)
  - `rds-iam-proxy.exe` (Windows)
- packaged archives per target (`.tar.gz`) that include:
  - platform folder (`rds-iam-proxy_<os>_<arch>`) with executable and docs
  - `config.example.yaml`
  - `README.md`
  - `LICENSE`
  - `THIRD_PARTY_NOTICES.md`


## License

This project is licensed under Apache-2.0. See `LICENSE`.

Third-party dependency notices are listed in `THIRD_PARTY_NOTICES.md`.

---

Built with care (and proudly vibe-coded) to solve a practical gap: I found only a few alternatives, they cost more than the value they delivered, and they didn't let me use the client of my choice.
