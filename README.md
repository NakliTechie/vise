# vise

> Holds the workpiece steady while the agent cuts. A language-agnostic CLI safety net for agent-led refactoring: freeze a codebase's observable behavior into a deterministic **behavior lockfile**, then gate any coding agent's refactor loop against it.

**Tier: Tool.**

## The problem

Unverified LLM refactoring fails roughly 60% of the time; the same refactoring gated by a verification layer approaches 98% correctness. The verification layer is where the value lives — yet no shipping tool packages it. What exists nearby is either a transform *engine* (OpenRewrite, codemods), a per-language golden-master *library* for humans (ApprovalTests), or a harness that baselines *the agent's* behavior rather than the app's. Nobody ships the net itself: language-agnostic, CLI-first, built to sit inside an agent's loop.

## What vise is

Three commands, one contract:

- **`vise record`** — freeze the current behavior: run the project's declared probes (commands, entry points, HTTP calls, rendered output) under determinism stubs (clock, RNG, network, locale) and write the results to a **behavior lockfile** (`vise.lock` — golden outputs, hashed and diffable).
- **`vise verify`** — replay every probe against the current working tree and diff against the lockfile. Output is **agent-legible**: exact probe, expected vs got, minimal diff — written for a model to act on, not a human to squint at.
- **`vise gate`** — `verify` with a hard exit code and a one-line verdict. The thing a refactor loop calls between every micro-step; the thing a CI job calls before merge.

The refactor loop it enables, for any agent:

```
vise record          # freeze — before touching anything
<agent refactors, one transform type per commit>
vise gate            # green → next step; red → revert, the diff says exactly what changed
```

## What vise is not

**Not a refactorer.** vise never edits code, never calls a model, and works as a plain golden-master regression tool with no AI anywhere near it (removable-AI by design). The agent is whatever you already use; vise is the deterministic stop it cannot argue with.

## Design anchors (from the research — see RESEARCH.md)

- **Behavioral equivalence over semantic equivalence.** Full equivalence is undecidable; same-outputs-on-a-defined-input-set is the production standard. The lockfile *is* the defined input set.
- **Determinism first.** A probe that can't be made deterministic (time, randomness, network) gets stubbed or it doesn't qualify as a probe.
- **Closed-loop micro-iterations.** The gate is cheap enough to run between every commit, because that cadence is what moves success from ~60% to ~98%.
- **The verifier lives outside the loop.** The agent being gated can run `vise gate` but must not be able to edit `vise.lock` or the probe definitions mid-run — re-recording is a human act.

## Next steps

See `plan/workplan.md` — spec first (probe manifest format, lockfile format, CLI contract), then the smallest end-to-end vertical: record + verify for shell-command probes on one real repo.
