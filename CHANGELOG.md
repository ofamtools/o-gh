# Changelog

## Unreleased

- Added: `pr.view` (`o gh pr view`, `g v` on the page): a Markdown view of a PR (title, number, repo, author, state, draft, mergeable, CI with counts, review decision and reviewers, comments, branch, labels, URL, description and checks). It declares a Markdown result, so mtm's page and the palette open it in the viewer; `pr.show` still answers the record. The poll now reads the PR body (not shown in the page's record pane).
- Added: page stories (`cmd/o-story/stories/gh/`, `TestPRStories`).
- Changed: ochazuke v0.84.0 to v0.86.0, so a key-bound text result opens the viewer (`keys.text-results`).
- Changed: PR checkout explicitly allows agents despite its owner confirmation prompt.
