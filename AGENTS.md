# o-gh: agent guide

`o-gh` (so `o gh`) is the family's GitHub PR tool: the PRs page (Mine, My Reviews, Watched, Ignored) and the signals it sends into the mtm inbox. It reads GitHub through the `gh` CLI only, one `gh api graphql --input -` request per poll, and never writes to GitHub (`pr checkout` is gh's local checkout).

It meets mtm only through each other's command line: mtm mounts the page from `o-gh catalog` and runs `o-gh pr VERB --target ID`; o-gh runs `mtm action-item add --source gh --url URL --why WHY` (spelled once, in `inboxCommand`). o-gh never imports a matomeru package or opens mtm's database.

## Plans and todos

Keep planning work in the family planning workspace, separate from this repository. Do not add a TODO.md or docs/plans/ to this repo.

## Where does X live

| Concern | File |
|---|---|
| `main`, the oden app, the shell's `--target` reveal, `o gh signals` | `main.go` |
| The gh runner, the GraphQL query (searches + one alias per watched PR), parsing, check states, `PRKey` | `gh.go` |
| Collections, ignore rules, CI from checks (`pr.ignore-checks`), `classify` | `collections.go` |
| The data directory: `state.json` (watched, ignored, allowed), the lock, atomic writes | `state.go` |
| `Tool`, the settings as a `Config`, the cached poll (`current`) | `tool.go` |
| Signal diffing, the inbox seam (`inboxCommand`, `mtmInbox`), `runSignals` | `signals.go` |
| The catalog: settings, nouns `pr`, `collection`, `signal`, verbs, key rows, the declared page | `catalog.go` |
| PR rows and cells, URL opener, clipboard | `rows.go` |
| The page on its own: `MountPage` over the tool's own catalog, reloads | `tui.go` |
| The logo | `docs/logo/build.py` |

## Invariants

- Read only against GitHub: every gh call is `api graphql` (a query) except `pr checkout`. Watched and ignored state is o-gh's own.
- Record first: `watch`, `ignore` and `send-to-inbox` work on a PR o-gh has never read (by URL); its row shows what was recorded until a poll answers it.
- What a person said about one PR beats a rule: watch and unignore override `pr.ignore`; watch and ignore exclude each other.
- A signal that fails to send stays for the next run; `--dry-run` writes nothing.
- Work that calls gh or the inbox runs off the loop (`ochazuke.Pending`); handlers read settings on the loop into a `Config`.
- Tests use the fake gh (`fixture_test.go`, from `testdata/poll.json`) and a fake inbox; no test reaches GitHub or mtm.
- Every commit: `gofmt -l . && go vet ./... && go test -race ./...` green (`GOWORK=off GOFLAGS=-trimpath`), `go run . catalog` answers. No amends.

Build: `go build -o ~/bin/o-gh .`. ochazuke (`v0.24.0`) and oden (`v0.1.1`) are required at their tags; an untracked `go.work` builds against the sibling checkouts.
