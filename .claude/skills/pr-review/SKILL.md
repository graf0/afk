---
name: pr-review
description: Turn the review findings and agent comments of a pull request into follow-up tickets. Use when the user gives a PR number and asks whether it needs more tickets, e.g. "/pr-review 72".
disable-model-invocation: true
---

# PR review

Given a PR number (open or merged), decide which review findings and open doubts deserve a **follow-up ticket**, which deserve only a comment on an existing issue, and which to drop. Output: a short proposal, then (after the user's go-ahead) the issues.

You do not review the code itself (that is `/code-review`) and you do not write the manual QA list (that is `/qa`). You read what is already said about the PR and turn it into tracker work.

The issue tracker, labels and domain docs are described in `docs/agents/`. Read `GLOSSARY.md` and any ADR touching the area first.

## Process

### 1. Gather

- `gh pr view <n> --json title,body,state,closingIssuesReferences,comments,reviews,files`, plus inline comments: `gh api repos/{owner}/{repo}/pulls/<n>/comments`.
- Issue comments of the PR: `gh api repos/{owner}/{repo}/issues/<n>/comments`. Agents summarise each closed ticket there, under headings like "Odstępstwa od speca", "Wątpliwości do sprawdzenia", "Pominięte w review", "Poprawione w review". These are the main source of findings.
- The closed issues (`Closes #N`) and their parent: acceptance criteria tell you what is a deviation from the spec and what is not.
- All issues, open and closed: `gh issue list --state all --limit 200`. Read the bodies of those that look related. Tickets titled "Wynik przeglądu PR #X" were already produced from earlier PRs; do not repeat them.

### 2. Sort every finding

Each finding ends in exactly one bucket:

1. **New ticket**: a real defect, a missing behaviour, or an open spec decision that someone has to make.
2. **Comment on an existing issue**: the finding extends an open issue (same cause, same fix). Do not open a duplicate.
3. **Manual QA**: needs a real machine, Windows, a browser, or a human watching. That is `/qa`'s job; do not make a ticket, only say so.
4. **Drop**: speculation about scale with no data (YAGNI), style nits already fixed in review, things the PR itself documents as intended, code outside the PR's diff, generated code.

Be critical. Most findings are bucket 3 or 4. A finding is a ticket only if you can say what concretely breaks or what decision is blocked. Group findings that share a fix or a decision into one ticket; do not file one ticket per bullet.

Also report, without ticketing, problems with the PR itself: review that was skipped or partial, AGENTS.md violations (new external dependency without asking, naming, error wrapping), leftovers (stashes, stray files), a `Closes` list that will close a parent with open children. Never edit the PR body yourself; propose it.

### 3. Spec decisions

If a finding is an open decision (semantics, naming, a syntax choice), do not guess and do not file it as `ready-for-agent`. Put the question to the user with your recommendation. After the answer, split the decision into implementation tickets, and record decisions that need no code in `docs/` (on the PR branch, if the PR is still open).

### 4. Show the proposal, then publish

Print in the terminal, in Polish:

- tickets: title, label, one-line reason, source finding (`#<ticket>` of the PR comment),
- comments on existing issues: target and text,
- dropped findings, one line each, with the reason,
- PR problems from step 2.

Wait for the user's go-ahead. Only then create anything. If nothing survives, say so and create nothing.

Tickets:

- Parent: the parent of the closed issues. `gh issue create --parent <parent> ...`; fall back to `Part of #<parent>` at the top of the body (see `docs/agents/issue-tracker.md`).
- Label: `ready-for-agent` when fully specified, `needs-info` when a decision is open (see `docs/agents/triage-labels.md`).
- Title in Polish, short, naming the behaviour, not the PR.
- Body in Polish, starting with `Wynik przeglądu PR #<n> (#<ticket>).`, then what happens today, then `Do zrobienia:` with a bullet list, including docs and tests to update. Name the dependency between tickets (`Zależy od #N`) when there is one.
- Create dependencies first, so the later body can cite the number.
- Label on the PR's own ticket: if any new ticket was created and the ticket the PR closes lacks `ready-for-agent`, add it (`gh issue edit <n> --add-label ready-for-agent`). "The PR's ticket" is the spec (the top-level closed issue, the one with sub-issues) or, when the PR closes a plain ticket, that ticket; not the sub-issues. Check with `gh issue view <n> --json labels`; no new ticket means no label change.
- PR back to draft: if any new ticket was created and the PR is open and not a draft, convert it back to a draft (`gh pr ready <n> --undo`), so ralph picks the spec up again and the PR cannot be merged before the new tickets land. Check with `gh pr view <n> --json isDraft,state`; a merged or closed PR, or no new ticket, means no change. Ralph marks the PR ready again after its review.
