# Issue tracker: GitHub

Issues and PRDs for this repo live as GitHub issues on the fork `tim-mcdonnell/cpa-usage-keeper`.
Use the `gh` CLI for all operations, and always pass `-R tim-mcdonnell/cpa-usage-keeper` explicitly.
This clone has an `upstream` remote pointing at `Willxup/cpa-usage-keeper`; bare `gh` commands can resolve to upstream, and nothing must ever be published there.

## Conventions

- **Create an issue**: `gh issue create -R tim-mcdonnell/cpa-usage-keeper --title "..." --body-file <file>`.
- **Read an issue**: `gh issue view <number> -R tim-mcdonnell/cpa-usage-keeper --comments`.
- **List issues**: `gh issue list -R tim-mcdonnell/cpa-usage-keeper --state open --json number,title,labels`.
- **Comment / label / close**: `gh issue comment`, `gh issue edit --add-label` / `--remove-label`, `gh issue close`, each with `-R tim-mcdonnell/cpa-usage-keeper`.

## Pull requests as a triage surface

**PRs as a request surface: no.**

## When a skill says "publish to the issue tracker"

Create a GitHub issue on `tim-mcdonnell/cpa-usage-keeper`.

## When a skill says "fetch the relevant ticket"

Run `gh issue view <number> -R tim-mcdonnell/cpa-usage-keeper --comments`.
