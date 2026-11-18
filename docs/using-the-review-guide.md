# Using AGENTS.md and the review guide

This repository has two files meant to be used while you write and review changes:

| File | What it is | Who reads it |
|---|---|---|
| `AGENTS.md` | Build and test commands, CI layout, and the hard rules that break enforcement, startup or upgrades when missed | Coding agents load it automatically (see [Tools](#tools)). Humans: read it once. |
| `docs/review-guide.md` | The full checklist: cache and API contracts, the PolicyEndpoint write path, precedents to reuse, recurring review themes. Each item is tagged `[blocking]` or `[advisory]` and cites code at a pinned commit. | Read the sections for the files your change touches. The routing table in section 1 maps files to sections. |

## While you write the change

1. **Find the sections that apply.** Look up the files you are changing in the routing table at the top of `docs/review-guide.md` and read those sections first. If you use a coding agent, ask it to do the same:

   ```text
   # A prompt for your coding agent before it starts editing
   Read AGENTS.md and the sections of docs/review-guide.md that cover the files
   this change will touch. List the [blocking] items that apply before you write code.
   ```

2. **Regenerate what the change touches.** After a change to kubebuilder markers or `api/v1alpha1/`, run `make manifests generate prepare-embed` and commit the regenerated files. Update the chart's `templates/rbac.yaml` and `crds/crds.yaml` by hand to match.

3. **Go through the hard rules in `AGENTS.md`** before you push. They are the checks that most often cost a review round: no writes to cached objects, no reads of fields the pod and Service transforms drop, an informer for every type read, PEs listed by the field index, PE changes that the node agent reads the same way, and flags and RBAC added to the chart.

## Before you open the PR

Go through the sections of `docs/review-guide.md` that the routing table names for your files, and check the items that apply to your change. Look up the relevant sections rather than handing the whole guide to an agent: the essentials are already in `AGENTS.md`.

If you ask an agent for a self-review, keep the guide out of its prompt and give it the severity bar instead:

```text
# A self-review prompt; run it on your branch before opening the PR
Review the diff between origin/main and HEAD. Report a finding as blocking only
if it makes a PolicyEndpoint allow what the policy denies or deny what it allows,
even briefly, leaves a PolicyEndpoint wrong after the next reconcile, grows without bound,
stops the controller from starting at scale, or breaks an upgrade or rollback.
Everything else is advisory; keep advisory findings to one line each.
For any finding about the informer cache, UnsafeDisableDeepCopy, rule identity
keys, CRDs or the PolicyEndpoint API, check the matching entry in
docs/review-guide.md before reporting it.
```

Then check the PR description:

- Which tests you ran: unit tests, and for resolver or write-path changes, cyclonus through `/test e2e <sha>` on IPv4 and IPv6.
- For scale or startup changes: the startup scale test result and before and after numbers.
- For changes to what a PolicyEndpoint contains: which node agent versions read it correctly, and what happens on upgrade from the last release and on rollback to it.

## When you review someone else's PR

The same applies: use the guide to check specific items the diff touches, not as a checklist to run in full. An item's tag is its worst case; rate each finding by whether it changes enforcement, its reach, and whether it self-corrects. Wrong enforcement is blocking even when it is temporary. An agent tends to report more concerns than a human reviewer would, so drop the ones you wouldn't raise yourself.

## Tools

Which tools load `AGENTS.md` without extra setup, per their documentation (checked November 2026):

| Tool | Loads `AGENTS.md` | Otherwise |
|---|---|---|
| Claude Code | Yes, when the repository has no `CLAUDE.md` | If you add a `CLAUDE.md`, start it with `@AGENTS.md` ([docs](https://code.claude.com/docs/en/memory)) |
| Kiro | Yes ([docs](https://kiro.dev/docs/steering/)) | |
| GitHub Copilot coding agent and code review on github.com, VS Code agent mode | Yes ([docs](https://docs.github.com/en/copilot/reference/custom-instructions-support)) | |
| Copilot code review in VS Code | No, it reads `.github/copilot-instructions.md` only | Paste the review prompt above, or point it at the files |
| Amazon Q Developer CLI | Not by default | Add `file://AGENTS.md` to your agent's `resources` |

## Keeping the guide current

- **Citations** are `path:line` at a named commit. When main moves far enough that lines drift, re-pin: check every citation at the new commit and update the commit named in section 1. Find a moved line by the quoted symbol.
- **New entries.** New contracts follow the section 3 shape (**Rule.**, **Why.**, **Cites.**, **Check.**); new precedents follow section 4. Each has a severity tag and a one-line bullet in the section 2 checklist. Add an entry when a review finds a repository-specific problem a strong general reviewer would likely miss. General review hygiene goes in the single list in section 2, not in new entries.
- **When code changes a contract.** A PR that changes behavior an entry describes (for example making `Cleanup` list by the field index) updates the entry in the same PR.
