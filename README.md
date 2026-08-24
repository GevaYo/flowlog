<p align="center">
  <img src="assets/logo.svg" alt="flowlog" width="340">
</p>

<p align="center">
  <b>Stop tailing eight terminal tabs.</b><br>
  Every local service log, merged into one timestamp-ordered stream you can follow, filter, and search.
</p>

A Go CLI that tails the `rapyd.log` files of your locally running services,
groups multi-line entries back into single logical entries, and merges them into
one timestamp-ordered stream in the terminal. It can also ship those entries to a
local OpenSearch so you get your QA Discover workflow (filter by `operation_id`)
on your own machine.

Services, repo paths, and aliases are read from `~/dev-services.json` (the same
file `dev-services` and the nvim debugger use). Set `FLOWLOG_CONFIG` to point
at a different file instead.

## Install

```bash
go install github.com/GevaYo/flowlog@latest   # into $(go env GOBIN) or ~/go/bin
```

Or from a clone:

```bash
git clone https://github.com/GevaYo/flowlog && cd flowlog
go build            # produces ./flowlog in the repo dir
go install          # or put it on PATH
```

## Optional: local OpenSearch for Discover

Only needed for the search half. Install and configure OpenSearch + Dashboards
once (`opensearch/setup.md` steps 1-2), then let flowlog do the rest:

```bash
brew install opensearch opensearch-dashboards   # see setup.md for the two config edits
brew services start opensearch opensearch-dashboards
flowlog setup                                   # template, index, index pattern, saved search
```

## Shell completions (zsh)

`flowlog completion zsh` prints a completion script covering every subcommand
and flag (with descriptions), and completes profile and service names from
your actual config. Install once:

```bash
mkdir -p ~/.zfunc && flowlog completion zsh > ~/.zfunc/_flowlog
# in ~/.zshrc, before compinit:
fpath=(~/.zfunc $fpath)
```

`flowlog setup` installs and refreshes the completion file automatically (and
`doctor` reports when it has gone stale after an upgrade); only the `fpath`
line is a one-time manual step.

## Checking your setup

```bash
flowlog doctor    # read-only: what is configured, what is broken, how to fix it
flowlog setup     # same checks, but repairs everything it can
```

Both print one line per check and exit non-zero if anything is wrong, so
`doctor` works in a script. `setup` is idempotent - run it any time Discover
misbehaves. The check most worth having: an index pattern whose field cache is
empty looks fine but makes Discover return no results at all.

Two of the checks aren't about Discover at all: `tmux installed` and
`aws cli + SSO profile` cover what `flowlog start` needs (see
"Service management" below).

| Flag (both subcommands) | Default | Meaning |
|---|---|---|
| `--os-url <url>` | `http://localhost:9200` | OpenSearch base URL |
| `--osd-url <url>` | `http://localhost:5601` | Dashboards base URL |
| `--index <name>` | `flowlog` | index prefix / saved object id |

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `-f` | off | follow live from EOF; omit for a post-hoc scan |
| `--op <uuid>` | "" | keep only entries with this operation id |
| `--since <dur>` | `1h` | post-hoc scan window before now (ignored with `-f`) |
| `--level <csv>` | all | comma-separated levels to include (error,warn,info,verbose,silly,debug) |
| `--profile <name>` | all | service profile from `dev-services.json` |
| `--services <csv>` | all | comma-separated service names or aliases |
| `--ship` | off | also ship entries to OpenSearch |
| `--os-url <url>` | `http://localhost:9200` | OpenSearch base URL |
| `--index <name>` | `flowlog` | OpenSearch index prefix — docs go to daily `<prefix>-YYYY.MM.DD` indices |
| `--retain <days>` | `7` | with `--ship`: delete daily indices older than this (0 = keep forever) |
| `--no-color` | off | disable ANSI color (also auto-off when stdout is not a TTY) |

## Usage

Follow every service in the `payment` profile live, merged into one stream:

```bash
flowlog -f --profile payment
```

Post-hoc: pull one flow by operation id out of the last hour of logs:

```bash
flowlog --op 3f2a9c14-8b7d-4e2a-9f10-6c5b1d2e3a4b
```

Shipping is idempotent: document ids are content hashes, so re-shipping an
overlapping window overwrites instead of duplicating. Daily indices plus
`--retain` keep the store bounded without any OpenSearch-side setup.

Ship a scan of the last 24h into local OpenSearch for Discover:

```bash
flowlog --ship --since 24h
```

## Service management (experimental)

flowlog also carries over `dev-services`, the tmux-based launcher that starts
your local services and gives them their own windows: `flowlog start`,
`flowlog attach`, `flowlog profiles`.

```bash
flowlog start                    # interactive picker
flowlog start -p payment         # named profile, no picker
flowlog attach -s dev-services   # attach to the tmux session
flowlog profiles                 # list profiles from dev-services.json
```

| Flag (`start`) | Default | Meaning |
|---|---|---|
| `-p`, `--profile <name>` | none | Use a named profile from `dev-services.json`, skipping the interactive picker |
| `--skip-aws-check` | off | Skip the AWS SSO credential check |
| `--debug` | off | Verbose output for the AWS credential check |

The AWS profile name is a placeholder: `awsProfile` in `aws.go` is set to
`your-aws-profile`. Change it to the profile in your own `~/.aws/config`, or
always pass `--skip-aws-check`.

Without `-p`/`--profile`, `start` lists every configured service and prompts
for a selection: numbers, names, or aliases, space- or comma-separated (empty
input cancels). Each selected service gets its own tmux window running its
command in its repo directory.

With `-p`/`--profile`, window 0 is renamed `logs` and runs this same flowlog
binary there in `-f --profile <name> --ship` mode, giving the session a
unified log view on open. Unlike the TS tool, it re-execs the running
binary's own path rather than looking `flowlog` up on PATH, so PATH setup no
longer matters for this to work.

`flowlog attach [-s <name>]` attaches to that tmux session (default name
`dev-services`). `flowlog profiles` lists the profiles and their services
from `dev-services.json`.

```bash
flowlog profiles add checkout -s collect_service,billing_service -d "Checkout flow"
flowlog profiles add checkout                    # interactive picker instead of -s
flowlog profiles remove checkout
```

| Flag (`profiles add`) | Default | Meaning |
|---|---|---|
| `-s <csv>` | none | Comma-separated service names or aliases; omit to pick interactively instead |
| `-d <text>` | "" | Profile description |
| `--force` | off | Overwrite an existing profile with this name |

Without `-s`, `profiles add` runs the same interactive picker as `start`.
`profiles add` and `profiles remove` rewrite `dev-services.json` with 2-space
indent and alphabetized object keys, preserving every field already in the
file, including ones flowlog itself doesn't read.

## More

- `opensearch/setup.md` - one-time install and configuration of local OpenSearch +
  Dashboards, the `flowlog` index template, and how to start/stop the services.
- `opensearch/bootstrap.sh` - wrapper that runs `flowlog setup`, falling back to
  `go run .` when the binary is not installed yet.
