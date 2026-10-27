# Issue tracker: GitHub

Issues and specs for this repo live as GitHub issues. Two environments, pick by what is installed (`command -v afk-forge` / `command -v gh`):

- **Host**: `gh` is available. Use it for everything (**Host: `gh`** below).
- **Chat** (VM without `gh`): `afk-forge` is available. Use it (**Chat: `afk-forge`** below). Operations marked _host only_ do not exist there: say so and hand them to the human instead of improvising.

Never use `gh` in Chat or `afk-forge` on the host.

## Host: `gh`

Infer the repo from `git remote -v`; `gh` does this automatically when run inside a clone.

- **Create an issue**: `gh issue create --title "..." --body "..."`. Use a heredoc for multi-line bodies.
- **Read an issue**: `gh issue view <number> --json number,title,body,labels,comments`.
- **List issues**: `gh issue list --state open --json number,title,body,labels,comments --jq '[.[] | {number, title, body, labels: [.labels[].name], comments: [.comments[].body]}]'` with appropriate `--label` and `--state` filters.
- **Make an issue a sub-issue of a parent**: `gh issue create --parent <parent> ...`, or `gh issue edit <parent> --add-sub-issue <child>` afterwards (`gh` 2.94+). Older `gh`: `gh api --method POST repos/<owner>/<repo>/issues/<parent>/sub_issues -F sub_issue_id=<child-db-id>` (database id, as in **Blocking** below). Without sub-issues, put `Part of #<parent>` at the top of the child body. A parent that afk should implement as a Spec gets the `spec` label (see `triage-labels.md`); without it, sub-issues are ignored and the parent is skipped with a warning.
- **Comment on an issue**: `gh issue comment <number> --body "..."`
- **Apply / remove labels**: `gh issue edit <number> --add-label "..."` / `--remove-label "..."`
- **Close**: `gh issue close <number> --comment "..."`

## Chat: `afk-forge`

Details and rationale: `docs/workflow.md` (`afk-forge`), ADR-0012. Flags: `afk-forge` without arguments prints usage. Output is plain text (no `--json`/`--jq`): `issue view` prints the ticket like `TICKET.md`, `issue list` prints `#N  [labels]  title`.

- **Create an issue**: `afk-forge issue create --title "..." --body "..." [--label a,b]`; multi-line body: `--body-file -` with a heredoc on stdin.
- **Read an issue**: `afk-forge issue view <number>`.
- **List issues**: `afk-forge issue list [--label <label>] [--state open|closed|all]` (default `open`). Need body or comments of each? `issue view` per number.
- **Comment on an issue**: `afk-forge issue comment <number> (--body "..." | --body-file -)`. Any issue.
- **Apply / remove labels**: `afk-forge issue edit <number> --add-label a,b --remove-label c`. Any issue.
- **Edit title / body**: `afk-forge issue edit <number> --title "..." (--body "..." | --body-file -)`, only on issues this Chat created, and only while they are not `ready-for-agent` (a human approved that text). Labels of such an issue stay editable.
- **Sub-issue of a parent**: _host only_. Put `Part of #<parent>` at the top of the child body. The `spec` label is allowed on create/edit.
- **Close**: _host only_.
- **Blocking, assignees, PRs**: _host only_ (see below).

Limits, short:

- `ready-for-agent` is replaced with `needs-triage` (a warning on stderr): a human grants readiness for the Factory. Tell the human which tickets you think are ready.
- The footer `— z Chat <name>` is appended automatically. Do not write it yourself.
- A comment on a `ready-for-agent` issue goes into the `TICKET.md` of the Factory's next Run on it: the Agent there acts on it unsupervised.

## Pull requests as a triage surface

_Host only (`gh`); Chat has no PR operations._

**PRs as a request surface: no.** _(Set to `yes` if this repo treats external PRs as feature requests; `/triage` reads this flag.)_

When set to `yes`, PRs run through the same labels and states as issues, using the `gh pr` equivalents:

- **Read a PR**: `gh pr view <number> --comments` and `gh pr diff <number>` for the diff.
- **List external PRs for triage**: `gh api --paginate 'repos/{owner}/{repo}/pulls?state=open' --jq '.[] | select(.author_association | IN("OWNER","MEMBER","COLLABORATOR") | not) | {number, title, author: .user.login, author_association, labels: [.labels[].name]}'`.
- **Comment / label / close**: `gh pr comment`, `gh pr edit --add-label`/`--remove-label`, `gh pr close`.

GitHub shares one number space across issues and PRs, so a bare `#42` may be either: resolve with `gh pr view 42` and fall back to `gh issue view 42`.

## When a skill says "publish to the issue tracker"

Create a GitHub issue (**Create an issue** for the environment you are in).

## When a skill says "fetch the relevant ticket"

Read it as in **Read an issue** for the environment you are in.

## Wayfinding operations

Used by `/wayfinder`. Dependencies, sub-issues, assignees and closing are host only, so in Chat use only the fallbacks (`Part of #<map>`, `Blocked by:` line, task list) and leave claim and close to the human. The **map** is a single issue with **child** issues as tickets.

- **Map**: a single issue labelled `wayfinder:map`, holding the Notes / Decisions-so-far / Fog body. `gh issue create --label wayfinder:map`.
- **Child ticket**: an issue linked to the map as a GitHub sub-issue (see **Make an issue a sub-issue of a parent**). Where sub-issues aren't enabled, add the child to a task list in the map body and put `Part of #<map>` at the top of the child body. Labels: `wayfinder:<type>` (`research`/`prototype`/`grilling`/`task`). Once claimed, the ticket is assigned to the driving dev.
- **Blocking**: GitHub's **native issue dependencies**, the canonical, UI-visible representation. Add an edge with `gh api --method POST repos/<owner>/<repo>/issues/<child>/dependencies/blocked_by -F issue_id=<blocker-db-id>`, where `<blocker-db-id>` is the blocker's numeric **database id** (`gh api repos/<owner>/<repo>/issues/<n> --jq .id`, _not_ the `#number` or `node_id`). GitHub reports `issue_dependencies_summary.blocked_by` (open blockers only, the live gate). Where dependencies aren't available, fall back to a `Blocked by: #<n>, #<n>` line at the top of the child body. A ticket is unblocked when every blocker is closed.
- **Frontier query**: list the map's open children (`gh issue list --state open`, scoped to the map's sub-issues / task list), drop any with an open blocker (`issue_dependencies_summary.blocked_by > 0`, or an open issue in the `Blocked by` line) or an assignee; first in map order wins.
- **Claim**: `gh issue edit <n> --add-assignee @me`, the session's first write.
- **Resolve**: `gh issue comment <n> --body "<answer>"`, then `gh issue close <n>`, then append a context pointer (gist + link) to the map's Decisions-so-far.
