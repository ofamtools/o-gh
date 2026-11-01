<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-animated-dark.svg">
    <img alt="o-gh" src="assets/logo-animated-light.svg" width="360">
  </picture>
  <br>
  <a href="https://github.com/ofamtools/o-gh/tags"><img alt="Latest tag" src="https://img.shields.io/github/v/tag/ofamtools/o-gh"></a>
  <a href="https://pkg.go.dev/ofam.dev/o-gh"><img alt="Go Reference" src="https://pkg.go.dev/badge/ofam.dev/o-gh.svg"></a>
  <a href="go.mod"><img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/ofamtools/o-gh"></a>
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/github/license/ofamtools/o-gh"></a>
</p>

<p align="center">The family's GitHub pull requests: yours, the ones waiting on your review, the ones you watch and the ones you ignore, on one page, with new review requests and CI changes sent to the mtm inbox.</p>

o-gh (`o gh` under [oden](https://github.com/ofamtools/oden)'s `o`, or `o-gh`) reads GitHub through the `gh` CLI you are already logged in to: one `gh api graphql` request answers both searches and every watched PR. It never writes to GitHub. What you watch and ignore is o-gh's own state, kept in its data directory.

## Install

Module `ofam.dev/o-gh`; requires Go 1.27 or newer and a logged-in [`gh`](https://cli.github.com).

```sh
go install ofam.dev/o-gh@latest
```

or, from a checkout:

```sh
gofmt -l . && go vet ./... && go test -race ./...
go build -o ~/bin/o-gh .
```

ochazuke (`v0.24.0`) and oden (`v0.1.1`) are required at their tags. An untracked `go.work` (`go work init . && go work use ../oden ../ochazuke`) builds against the sibling checkouts; `GOWORK=off` builds from the tags as a fresh clone does.

## Usage

```sh
o-gh                                    # the PRs page (also o-gh view; --smoke renders one frame)
o-gh pr mine|reviews|watched|ignored    # one collection's rows
o-gh pr list [--collection C] [--fresh] # every PR once, or one collection
o-gh pr show|open|open-files|copy-url|copy-branch --target PR
o-gh pr watch|unwatch|ignore|unignore --target PR
o-gh pr checkout [DIR] --target PR      # gh pr checkout, after a confirm (--yes)
o-gh pr send-to-inbox [WHY] --target PR
o-gh pr refresh                         # poll now; the counts
o-gh collection list|show|open          # show and open take --target collection/ID
o-gh signals [--dry-run]                # new review requests and CI changes to the mtm inbox
o-gh settings list|set|reset            # the settings below
o-gh catalog                            # the nouns, verbs and page, for mtm
```

`PR` is a PR's URL, `OWNER/REPO#N` or its row id `pr/OWNER/REPO#N`. A list verb answers rows (`{noun, id, label, data, cells}`); `--format table|markdown|toon` prints the cells.

The examples use invented PRs (the tests' fixture, `testdata/poll.json`):

```console
$ o-gh collection list --format table
Collection  PRs  About
Mine          2  Your open PRs and drafts (pr.mine-query)
My Reviews    2  Open PRs that ask for your review (pr.reviews-query)
Watched       0  PRs you watch: pr watch; their CI changes are signals
Ignored       0  PRs you ignored or an ignore rule (pr.ignore) matches: no signals
$ o-gh pr mine --format table
PR                   Title                      Author  CI      Review        Updated
tea-shop/kitchen#12  Add the rice cooker timer  miso    ✗ fail  waiting on 2      20h
tea-shop/menu#3      [draft] WIP: tea menu      miso    … run   -                  2d
$ o-gh settings set pr.ignore '["author:dependabot*"]' >/dev/null
$ o-gh pr ignored --format table
PR                   Title                         Author           CI      Review        Updated
tea-shop/kitchen#41  Bump steeper from 1.2 to 1.3  dependabot[bot]  ✓ pass  waiting on 1      18h
$ o-gh signals --dry-run | jq -c '.signals[] | {kind, why}'
{"kind":"review-requested","why":"review requested: tea-shop/kitchen#40 Bump leaf grade (by nori)"}
```

## Concepts

### Collections

| Collection | What is in it |
|---|---|
| Mine | the `pr.mine-query` search: your open PRs and drafts |
| My Reviews | the `pr.reviews-query` search: open PRs that request your review (a team's request included) |
| Watched | the PRs you watch (`pr watch`), open or not; o-gh fetches each by number |
| Ignored | the PRs you ignore (`pr ignore`) and those an ignore rule matches |

A PR can be in Mine and Watched at once. An ignored PR is in Ignored only and sends no signals. What you said about one PR beats a rule: watching a PR, or unignoring one a rule matches (o-gh then records the exception), keeps it out of Ignored. Watching and ignoring exclude each other.

### Ignore rules

`pr.ignore` is a list of rules. A rule is terms that must all match: `draft`, `repo:GLOB`, `author:GLOB`, `label:GLOB`, `base:GLOB`, `head:GLOB` (`*` is any text, `?` one character; case-insensitive) and `title:TEXT` (a substring). `["author:dependabot*", "repo:tea-shop/* draft"]` ignores the bot's PRs and the drafts in tea-shop.

`pr.ignore-checks` lists check names (globs) that CI leaves out, for optional or flaky checks. A PR's CI is `fail` when a counted check failed, else `pending` while one runs, else `pass`; with no checks it is the rollup's state, or `none`.

### Signals

`o-gh signals` polls once, compares with the last run and sends each new item to the mtm inbox:

- **review-requested**: a PR in My Reviews that the last run did not see there. On the first run every open request is new.
- **ci**: a PR in Mine or Watched whose CI became `fail` or `pass` (a change to `pending` is not a signal). The first run records the states only.

Each one runs

```sh
mtm action-item add --source gh --url URL --why WHY
```

`$O_GH_MTM` names another mtm and `$O_GH_MTM_ARGS` adds words before `action-item` (for example `--data /tmp/x.db`). A signal that fails to send is kept for the next run. `--dry-run` prints the signals and the commands, sends nothing and leaves the last run as it was. Ignored PRs send nothing; an ignored PR's CI state is still recorded (unignoring it replays no old change) and its review request is not (unignoring it sends it). Run it from a schedule (mtm's, cron or a loop) to have signals arrive without a key press.

### The poll and the cache

The four lists read one cached poll (`poll.max-age`, default 60s); a watch the cached poll did not ask for, or a change to a search, polls again. A lock in the data directory makes lists loading at once poll once. `--fresh`, `pr refresh` and `o-gh signals` always poll. A watched PR the poll cannot find stays in Watched, marked stale, with the poll's error in `pr refresh`.

### Settings and data

| Setting | Default |
|---|---|
| `pr.mine-query` | `is:pr is:open author:@me archived:false sort:updated-desc` |
| `pr.reviews-query` | `is:pr is:open review-requested:@me archived:false sort:updated-desc` (`user-review-requested:@me` leaves out team requests) |
| `pr.limit` | 50 PRs per search |
| `pr.ignore`, `pr.ignore-checks` | none |
| `poll.max-age` | `60s` |

Settings live in `~/.config/o-gh/settings.json` (`$XDG_CONFIG_HOME`). The data directory (`$O_GH_DATA`, else `$XDG_DATA_HOME/o-gh`, else `~/.local/share/o-gh`) holds `state.json` (watched, ignored, unignored), `poll.json` (the last poll) and `signals.json` (the last signals run). `$O_GH_GH` names another gh, `$O_GH_OPEN` the URL opener and `$O_GH_CLIPBOARD` the clipboard command.

## The PRs page

The catalog declares it as a data page (layout `split`: the lists Mine, My Reviews, Watched and Ignored, each from its source verb `pr.mine` and so on, beside the selected PR's record), so mtm draws it from o-gh's rows. `o-gh` mounts the same page over itself with ochazuke's `MountPage`, as a host does, and reloads it after a change and every `poll.max-age`.

| Key | Does |
|---|---|
| `enter` | open the PR in the browser |
| `y y` | copy its URL |
| `w` / `W` | watch / unwatch |
| `d d` | ignore (it moves to Ignored) |
| `g i` | send it to the mtm inbox |
| `g c` | check it out here (`gh pr checkout`, after a confirm) |
| `j` `k` `}` `{` | move, through the lists |
| `:` | the palette: every verb, `pr unignore` and `pr show` included |
| `?` | every key the focus answers |
| `q` | quit (o-gh's own page only) |

The key rows are the catalog's, so mtm's page answers them too, and `bindings.json` can change them.

## In the family

- **mtm.** o-gh is an extension: mtm mounts the PRs page from `o-gh catalog` and runs its verbs as `o-gh pr VERB --target ID`. Signals go in through mtm's command line only (`mtm action-item add`); o-gh never opens mtm's database.
- **o-git.** o-git is local git (worktrees, branches) and never touches the network; o-gh is GitHub and never touches a repository except `pr checkout`, which is gh's.
- **ochazuke and oden.** `o gh` is an oden subcommand of `o`; the `pr`, `collection` and `signal` nouns are an ochazuke runtime.

## Logo

The logo is a pull request, two commits on a branch and a third curving back to it, in violet, GitHub's merge colour; discs and stems sit on the 8-unit grid so the icon stays a PR at 16 px. `python3 docs/logo/build.py` writes `assets/icon.svg`, `assets/logo-light.svg` and `assets/logo-dark.svg`, with the wordmark in the family's lettering (`--check` confirms the glyphs still redraw ochazuke's and matomeru's).

MIT license.
