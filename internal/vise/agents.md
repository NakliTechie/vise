# Working in a repository gated by vise

This file is written for a coding agent. It is also a template: copy it to the
root of any repository you gate with vise, adjust the project-specific lines at
the end, and the agent working there will know the rules.

Your changes are judged by replaying a frozen set of observations. The gate is
the source of truth about whether you broke something — not your reading of the
code, not the tests alone, not your confidence.

## First move, every session

```sh
vise version --json     # which build am I talking to?
vise status --json      # what is the situation?
vise gate --json        # is it green before I touch anything?
```

`status --json` carries a `tool` object (version, revision, modified), so one
read tells you both the situation and which binary is reporting it. `version
--json` carries the same identity on its own. The `version` string alone cannot
tell two builds apart, so check the revision whenever the tool behaves oddly,
and always when a lockfile will not parse.

`status` exits 0 for any call it understands, and reports the whole situation: whether a baseline
exists, whether it agrees with the manifest, whether the environment drifted,
whether a rerun is refused, and exactly one `next.action`. Read it before you
touch anything.

**Then gate once, before your first edit.** `status` reads state; it does not
run a probe, so it cannot tell you whether the suite passes right now. Without
that first verdict you cannot tell a failure you caused from one that was
waiting for you, and the two have opposite responses: fix your change, or stop
and report a blocked repository. It costs one run.

**Know which binary you are running.** `vise` comes from your `PATH`, and the
one there may be older than the repository, or built from somebody's dirty
tree. `vise version --json` tells you: `version`, `revision`, and `modified`. A
binary built without VCS stamps — an install from a tarball, say — carries no
revision, so both fields are absent rather than false. Absent means unknown,
and report it that way: a build that cannot tell you whether its tree was
clean has not told you that it was.

- Put the revision and `modified` in your final report, always. An operator
  reading a green gate needs to know which build produced it, and that costs
  you one line.
- If `modified` is `true`, the binary was built from a tree that had
  uncommitted changes, so its behaviour matches no commit. Say so; that is
  information an operator will want and cannot recover later.
- If the tool behaves oddly, or a lockfile will not parse, or you are told the
  baseline was `written by a newer vise`, the tool is the suspect and not the
  code. Stop and report the revision.

You are not expected to build or install vise. Which binary is on the `PATH`
is the operator's decision; saying which one you used is yours.

## The loop

```sh
# one focused change
vise gate --json
git commit -m "..."        # when, and only when, the gate is green
```

Gate after every change, before moving on. It costs seconds. Do not batch five
changes and gate once: the gate's value is that it tells you *which* change
broke something, and batching throws that away.

**Commit each green step yourself.** Do not ask permission to commit; that is
the loop. Commits also matter mechanically here — the rerun budget is keyed to
the commit, so committing is how a stuck loop gets a fresh start.

If committing is denied — some sandboxes refuse writes to `.git`, and you will
see `Operation not permitted` on `.git/index.lock` — say so once, keep your work
in the working tree, and carry on with the task. A denied commit is a fact to
mention in your final report, not a reason to stop. Gate before each step
regardless; the verdict is what matters, the commit is bookkeeping.

## Branch on the exit code, never on the prose

**This table is for `gate` and `verify`.** They are the commands that judge a
change, and their exit code is the verdict.

`status`, `doctor` and `version` report a situation rather than judging one, so
a valid call to any of them exits 0 whatever it finds — read `next.action` and
ignore the code. An agent that treats their exit 0 as "all is well" walks
straight past a `status` that just told it there is no baseline.

A call they cannot understand is different: an argument they do not take is
exit 2, because that is a complaint about your command line rather than a
report about the repository. So exit 0 from those commands means *the report is
in `next.action`*, and a nonzero one means *you asked wrongly and got no
report at all*. Do not read a usage error as a situation.

| exit | verdict | meaning | what you do |
|---|---|---|---|
| 0 | green | every declared observation matched | commit, next step |
| 1 | red, `behavior` | you changed what the code does | revert your change |
| 2 | indeterminate, `harness` | the judge is broken or its inputs moved | see below — never touch the code under test |
| 3 | indeterminate, `flake` | an observation was unstable | `quarantine_ack` — stop and report |
| 4 | indeterminate | no baseline exists | stop and report |
| 5 | red, `metric` | behavior held, a tracked metric got worse | revert what worsened it |

The JSON always carries `exit`, `verdict`, `counts` and one `next.action`.
`classes` and `failures` appear only when something failed — a green outcome has
neither, because there are none. Failures are keyed by the name of the thing
that failed: usually a probe id, but `manifest`, `journal`, `vise.lock`,
`fingerprint` and `rerun-limit` name themselves. Read those fields. Never parse
the human text.

The closed vocabulary, in full, so you can write the switch: `proceed` ·
`revert` · `fix_probe` · `human` · `record_first` · `quarantine_ack`. Six values
and no seventh. If you ever receive one that is not on this list, that is a
defect in vise — stop and report it rather than guessing what it meant.

## Exit 1 — you changed behavior

`vise verify` prints a diff for every probe that diverged, each showing the
first line where that probe's output stopped matching. Those diffs are the
truth about what you changed. Read them, then revert.

A refactor that changes observable behavior is a failed refactor even when the
new behavior is better. If you are confident the change is right, that is a
conversation, not a decision you make: stop, say so in your final message, and
leave the baseline alone.

## Exit 2 — the harness broke

The message names the cause. The response is always to restore the harness,
never to edit the code under test so the gate passes.

Two harnesses, and only one of them is yours. A probe command your change broke
is yours to fix. `vise.toml`, `vise.lock`, `.vise/blobs/`, and the journal are
not, and the rules below forbid you from writing them — so when the repair is
in one of those, the tool says `next.action: human` rather than `fix_probe`,
and your move is to stop and report. You should never be in a position where
obeying `next.action` means breaking a rule; if you ever are, that is a defect
in vise and worth saying so.

**Exit 2 has two halves, and one field separates them.** The JSON failure
carries `operator: true` when the repair lives in a file you may not write.
Read that rather than matching on the message:

| `next.action` | `operator` | whose repair | what you do |
|---|---|---|---|
| `fix_probe` | absent | yours | a probe command your change broke; fix your change |
| `human` | `true` | the operator's | `vise.toml`, `vise.lock`, `.vise/blobs/`, the journal, or the recorded environment; stop and report |

The message table below is for reading, not for branching. If you ever get
`fix_probe` on a failure whose only repair is in one of those files, that is a
defect in vise: say so, and stop rather than choosing which instruction to
disobey.

| message | cause | your move |
|---|---|---|
| `probe definition changed after recording` | you edited `vise.toml` | revert that edit |
| `declared probe input changed after recording` | you edited a file a probe consumes as a fixture | revert it |
| `metric definition changed after recording` | you edited a metric's definition | revert it |
| `environment differs from recording` | the toolchain or platform moved | **stop and report** — a human re-records |
| `probe modified vise state` | something you did wrote to the judge's own files | revert it |
| `probe modified git's own state` | a probe moved `HEAD`, or changed the ignore rules or Git config | revert it; the checkout is judged against those, so changing them changes what unchanged means |
| `wrote files git neither tracks nor ignores` | a probe left a stray file in the checkout | the message names the files; remove the write, or ask an operator to add those paths to `.gitignore` |
| `could not be launched` / `timed out` | the probe's command is broken | if your change broke the build, fix your change; if it was broken before you started, **stop and report** |
| `is tracked by git` | a declared artifact is a tracked file | **stop and report** — the manifest needs an operator |
| `written by a newer vise` | the `vise` on your PATH is older than the baseline | **stop and report**: the tool is stale, not the code. `vise version --json` names the build you are running |

**If the gate was already failing before your first edit, you are blocked.**
Say so immediately and stop. Do not spend the session investigating the
environment; do not build your own copy of the tooling to work around it. A
blocked repository is a fact to report, not a puzzle to solve.

## Exit 3 — flake

An observation differed between two runs of the same code. The gate calls this
neither green nor red, on purpose. A probe may flake twice at one commit and
lock; the third run is refused with `next.action: human`. That is one rerun
after the run that first flaked, not two.

Rerunning until it passes is the one thing you must not do. Stop and report.

## Rules you do not break

1. **Never edit `vise.toml`, `vise.lock`, `.vise/blobs/`, or
   `.vise/journal.jsonl`.** These are the judge. Be clear about what happens if
   you do: vise cannot authenticate its caller, so it will simply believe the
   edited baseline and report green. What catches it is the human reading
   `git diff`, and CI comparing the printed `lock:` hash against the one
   recomputed from the trusted branch. Editing them does not fool the judge so
   much as remove it, which is worse than the failure you were trying to avoid.
2. **Never run `vise record`.** Freezing a baseline is an operator action. If
   you believe the baseline is wrong, say so and stop.
3. **Never weaken a probe** to make it pass — not by narrowing what it observes,
   not by deleting it, not by relaxing a comparison, and not by widening a
   normalizer. That last one is the quiet version: a pattern that turns every
   `- name: ok` into `- FINDING` makes a flaky probe stable and makes it blind,
   and the gate then reports green while the program is broken.
4. **Never delete or skip a test** for the same reason.
5. **Never edit a design document to make your change legal.** The
   specification, the non-goals, the architecture notes: those record decisions
   somebody made on purpose. If your change needs one of them to say something
   different, you have not found a way to do the task — you have found that the
   task requires a decision you were not asked to make. Say which decision, and
   stop.

Reaching for any of these means the answer is to revert your own change instead.

## When the task and the rules conflict

Some tasks cannot be completed without doing something these rules forbid.
Making the gate faster by narrowing a probe. Changing output the baseline
freezes. Removing a check that fails. Implementing something the specification
lists as a non-goal. Recognize that early rather than late:
name the rule the task would require you to break, say what an operator could do
instead — re-record a baseline, change the manifest, accept a slower gate — and
stop.

An hour spent looking for a way around the gate is worse than one minute spent
saying the task needs an operator. You are not failing the task by reporting
this; you are doing the only useful thing left.

## Say when the gate did not really check you

A green gate means every declared observation matched. It does not mean your
change is right. It means no probe noticed — and if no probe walks the code you
touched, none would have noticed had you broken it.

So before you report a change as green, ask which probe would have gone red if
the change were wrong. If the honest answer is *none*, say so in your final
message. Name the change and say no declared observation covers it. You are not
admitting a mistake; you are telling the operator which part of your work the
gate did not vouch for, which is the difference between a review that can be
short and one that has to be long.

This matters most for the changes that look safest: an internal helper nothing
prints, a lookup nobody's probe exercises, a path taken only on an error
nothing triggers. Those are exactly the changes a green gate says nothing
about.

**You can answer that question mechanically instead of guessing.** Break the
code you just changed, on purpose, and gate:

```sh
# change the string, invert the condition, return the wrong value
vise gate --quiet        # red, naming a probe → that probe covers your change
                         # green                → nothing covers it; now you know
git checkout -- <file>   # restore before doing anything else
```

It costs one gate run and turns a judgement call into a fact. Two ways it lies,
both of which cost me a wrong conclusion while writing this paragraph:

- **The mutation must still compile.** Deleting a line that leaves a variable
  unused fails the build, so every probe goes red and you learn nothing about
  coverage. Change a format string or a constant, not the shape of the code.
- **Confirm the edit landed.** A search-and-replace that matched nothing, or
  matched three places when you meant one, leaves the tree unchanged and the
  gate green — and green is exactly the answer you were hoping not to see.
  Print the changed line before you gate.

Restore before you continue, and gate again to prove you did.

## Say what looks wrong, without fixing it

A refactor makes you read code carefully that nobody has re-read in a while.
When you finish, you are holding context an auditor would have to rebuild from
nothing. Spend one paragraph of your final message on it.

If you noticed something that looks wrong — a case that is not handled, an
output that could mislead the person reading it, an ordering that contradicts
what the comment above it claims, a test that would pass even if the thing it
names were broken — say so under a heading of its own. Say what you saw and
where. Do not fix it: that is a separate decision, and a finding that arrives
as a change inside a diff somebody is reviewing for another reason is worse
than no finding.

This has produced a real defect nearly every time it has been asked for in this
project, including in code written hours earlier by someone who was being
careful.

## When you finish

State plainly, in this order: the gate's final verdict and exit code; the
`revision` and `modified` from `vise version --json`, so a reader knows which
build produced that verdict; what you changed; anything you changed that no
probe covers; anything that looked wrong while you were in there; what you
could not do and why. If the gate is not green, say that first. A red gate is never "done", and
neither is a green gate you reached by narrowing what is checked.
