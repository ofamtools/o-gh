# Changelog

## Unreleased

- Changed: requires ochazuke v0.110.1 (esc only leaves insert in modals and `q` closes them; label-first pickers).

## v0.2.0 (2026-11-08)

- Changed, breaking (lexicon, ochazuke v0.105.0, one shot, no aliases; every rename is in `docs/lexicon-renames-v0.105.tsv`): the `collection` noun is gone, its collections (mine, reviews, oncall, watched, ignored) are saved filters on `pr` (a `collection` prop holds a PR's collections) and `collection open` is `pr open-search --collection C`; `pr view` is `pr show` (the Markdown view) and the old `pr show` record is `pr get`; `copy-url` and `copy-branch` are `yank-url` and `yank-branch`; every `state` field of a PR, check, review and signal snapshot is `status` (GitHub's own `state` is still what the GraphQL answer says), and an older `poll.json` or `signals.json` is read with `state` as `status` and written back so; the row props `ignored` and `reviews` are `is_ignored` and `review_list`, and `in` is `collection`; `pr` is the main noun. `TestLexicon` runs with an empty baseline.
- Changed: requires ochazuke v0.102.0 and oden v0.3.0 (the Operations registry and three-part keys; `cmd/opname` moved the code). o-gh declares its namespace, `gh` (`ochazuke.DeclareNamespace`), so its keys are `gh.<noun>.<verb>` and the catalog carries `namespace: gh`. CLI spellings are unchanged.
- Added: `pr.view` (`o gh pr view`, `g v` on the page): a Markdown view of a PR (title, number, repo, author, state, draft, mergeable, CI with counts, review decision and reviewers, comments, branch, labels, URL, description and checks). It declares a Markdown result, so mtm's page and the palette open it in the viewer; `pr.show` still answers the record. The poll now reads the PR body (not shown in the page's record pane).
- Added: page stories (`cmd/o-story/stories/gh/`, `TestPRStories`).
- Changed: ochazuke v0.84.0 to v0.86.0, so a key-bound text result opens the viewer (`keys.text-results`).
- Changed: PR checkout explicitly allows agents despite its owner confirmation prompt.
