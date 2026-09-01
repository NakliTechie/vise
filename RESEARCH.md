# Research — agentic refactoring, folded 2026-09-01

Findings from the survey that motivated vise. Each claim carries its source; numbers verbatim from the cited material.

## Headline numbers

- Raw AI refactoring fails ~60% of the time; paired with a verification layer, correctness climbs to ~98%. — [Janea Systems, "Flipping the Odds"](https://www.janeasystems.com/blog/ai-and-refactoring-part-2)
- Closed-loop micro-iterations (change → check → repair) reach 90%+ unit-test pass rates vs ≤60% for single-shot LLM refactoring (RefAgent vs single-shot). — [Agentic Refactoring overview](https://www.emergentmind.com/topics/agentic-refactoring)
- Empirical study of ChatGPT/Claude/Cursor refactoring PRs on real repos: recurring failure modes are subtle behavioral changes, type violations in statically-typed contexts, **incomplete transformations** (half-refactored code left behind), and context misunderstanding. — [Agentic Refactoring: An Empirical Study](https://arxiv.org/pdf/2511.04824)

## The verification doctrine

- **Full semantic equivalence is undecidable; production systems adopt behavioral equivalence** — same outputs as the original on a defined input set, via replay/equivalence checking. — [InfoWorld, deterministic modernization primer](https://www.infoworld.com/article/4073173/a-practitioners-primer-on-deterministic-application-modernization.html)
- The classical method is the **characterization / golden-master test** (Michael Feathers): record what the code *actually does* — not what it should do — before touching it, and lock it. Precondition: make the code deterministic (stub clock, RNG, external resources). — [Wikipedia](https://en.wikipedia.org/wiki/Characterization_test), [golden-master walkthrough](https://chicio.medium.com/golden-master-testing-aka-characterization-test-a-powerful-tool-to-win-your-fight-against-legacy-1ca590f219a1)
- Multi-stage verification in practice: AST-level check that a commit contains *only* the claimed transformation type; full test-suite execution for behavioral equivalence; sampled manual checks against automated false positives. Static-analysis gates (CodeQL/Sonar-class) before merge; formal (SMT/type-based) proofs reserved for critical transforms. — [EmergentMind](https://www.emergentmind.com/topics/agentic-refactoring)

## The industry pattern at scale: hybrid

- **Deterministic engine for the bulk, LLM for the residual.** Recipes/codemods execute mechanical transformations reproducibly; the model is called only where developer-like adaptation is needed (diagnosing the build failure the recipe couldn't resolve). — [HatchWorks](https://hatchworks.com/blog/gendd/modernization-at-portfolio-scale/), [Deterministic Code in the Loop](https://ctoadvisor.substack.com/p/deterministic-code-in-the-loop-361), [The New Stack](https://thenewstack.io/why-ai-alone-fails-at-large-scale-code-modernization/)
- Why: in long-horizon migrations an agent makes hundreds of interdependent probabilistic decisions — **variance compounds**; determinism is a project-management necessity. Google's internal LLM-assisted migrations run this same shape. — [Google, "How is Google using AI for internal code migrations?"](https://arxiv.org/pdf/2501.06972)
- Process hygiene the sources converge on: **one refactoring type per commit**; refactor commits never mixed with feature/fix commits; incremental git commits with plan-level rollback; containerized/sandboxed execution.

## Prior art map (why vise's slot is open)

| Neighbor | What it is | Why it isn't vise |
|---|---|---|
| [OpenRewrite](https://github.com/openrewrite/rewrite) / Moderne | Deterministic transform engine (lossless semantic trees, recipes), JVM-centric | The *cutter*, not the net; no behavior lockfile |
| [codemod CLI](https://github.com/codemod/codemod) | Codemod scaffolding/sharing | Transform authoring; no verification contract |
| ApprovalTests family | Per-language golden-master libraries | Library for humans writing tests, not a language-agnostic CLI in an agent loop |
| EvalView / AgentV-class harnesses | Baseline-diff an *AI agent's* behavior | Wrong subject — they gate the agent, not the codebase |
| [REM2.0](https://arxiv.org/pdf/2601.19207) | Automated equivalence proofs for Rust extract-method | Formal, single-language, research-stage |
| [cc-safety-net](https://github.com/kenryu42/cc-safety-net) / guardrail hooks | Block destructive commands pre-execution | Command guardrails, not behavioral equivalence |
| MatchFixAgent, LASSI-EE (LLM-as-judge equivalence) | LLM judges functional equivalence of translations | Probabilistic judge — vise exists to be the *deterministic* stop |

## Implications baked into vise's design

1. `record`/`verify`/`gate` — the lockfile is the "defined input set" of the behavioral-equivalence standard.
2. Determinism stubs are first-class, not an afterthought — a probe that can't be stubbed deterministic doesn't qualify.
3. Gate output is agent-legible (probe · expected · got · minimal diff) because the consumer of a failure is a model mid-loop.
4. The gated agent must not be able to edit the lockfile or probes mid-run — re-recording is a human act (the evaluator lives outside the loop, or the loop reward-hacks its own gate).
5. vise carries no model and no transform engine — it composes with codemods/agents rather than competing (removable-AI).
