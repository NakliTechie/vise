# Research — agentic refactoring, folded 2026-09-01

Findings from the survey that motivated vise. Each claim carries its source, and
numbers are verbatim from the cited material.

That sentence was not true until 2026-09-03. Every link here was fetched and read
against the claim beside it; six claims were wrong and are corrected below, each
with a note saying what it used to say and why it was wrong, so nobody
reconstructs the old version from a half-memory. Two sources could not be read at
all and are marked unverified rather than quietly trusted. A survey nobody
re-reads decays into the plausible, and the plausible is what a tool like this
exists to distrust.

## Headline numbers

- Raw GPT-3.5 "produces correct refactorings 26-33% of the time depending on the code smell category (with GPT-4 only slightly better at the expense of speed and cost)"; with a verification layer the "shipped-to-production rate climbs to 98% because the 60-something percent of bad outputs never make it past the filter", and "96-99% of what passes through is correct". — [Janea Systems, "Flipping the Odds"](https://www.janeasystems.com/blog/ai-and-refactoring-part-2)
  - This line previously read "fails ~60% of the time", which is not what the source says: 26-33% correct is a 67-74% failure rate. The 60 came from the source's own phrase about bad outputs not passing the filter. Checked against the page 2026-09-03.
- Closed-loop micro-iterations (change → check → repair) reach 90%+ unit-test pass rates vs ≤60% for single-shot LLM refactoring (RefAgent vs single-shot). — [Agentic Refactoring overview](https://www.emergentmind.com/topics/agentic-refactoring)
- Large-scale study of agent-generated refactorings (OpenAI Codex, Claude Code, Cursor) in **open-source Java projects**: 15,451 refactoring instances across 12,256 pull requests and 14,998 commits. Agents skew to small, local, type-and-name changes (Change Variable Type 11.8%, Rename Parameter 10.4%, Rename Variable 8.5%), stated motivations are maintainability (52.5%) and readability (28.1%), and the negative finding is that "agents currently fail to consistently reduce the overall count of known design and implementation smells". — [Agentic Refactoring: An Empirical Study](https://arxiv.org/pdf/2511.04824)
  - This line previously attributed a four-item taxonomy of failure modes to this paper — subtle behavioral changes, type violations, incomplete transformations, context misunderstanding. **The paper contains no such taxonomy.** It also said ChatGPT rather than Codex, and "real repos" rather than Java. Checked against the full text 2026-09-03. The failure modes may well be real; they are not this paper's finding, and nothing here should be cited for them.

## The verification doctrine

- **Full semantic equivalence is undecidable, so production systems settle for behavioral equivalence** — same outputs as the original on a defined input set. The undecidability is Rice's theorem and needs no industry citation; the practice of settling for observed behavior is what the characterization-test line below documents. **No source here is cited for it**, because the one that used to be does not say it.
  - This line cited an InfoWorld primer that contains none of `undecidable`, `semantic equivalence`, `behavioral equivalence`, `replay`, or `equivalence checking` — it is an OpenRewrite article about lossless semantic trees and recipes, and its "deterministic" means the transform is repeatable, which is a different claim. Checked twice 2026-09-03.
- The classical method is the **characterization / golden-master test** (Michael Feathers): record what the code *actually does* — not what it should do — before touching it, and lock it. Precondition: make the code deterministic (stub clock, RNG, external resources). — [Wikipedia](https://en.wikipedia.org/wiki/Characterization_test), [golden-master walkthrough](https://chicio.medium.com/golden-master-testing-aka-characterization-test-a-powerful-tool-to-win-your-fight-against-legacy-1ca590f219a1) (**403 on every attempt 2026-09-03; unverified**. Wikipedia alone supports the weaker "volatile and non-deterministic values need to be masked / removed" rather than the clock/RNG/external-resource list)
- Multi-stage verification in practice: test-suite execution to "ensure correctness and adherence to intended refactoring types", "static-analysis gates (e.g., CodeQL, SonarQube) that preempt merging of regressions", and "Formal verification—e.g., SMT- or type-based proofs—can be integrated for critical transformations". ASTs appear in *planning*, not in commit verification. — [EmergentMind](https://www.emergentmind.com/topics/agentic-refactoring)
  - **The equivalence mechanism this page describes is "built-in semantic equivalence checks via LLM-as-Judge agents"** — a model judging whether behavior was preserved. That is precisely what vise refuses to do, and it belongs in the prior-art argument rather than being left out of it.
  - This line previously claimed an AST-level check that a commit contains only the claimed transformation type, and sampled manual checks against false positives. Neither is on the page. Checked twice 2026-09-03.

## The industry pattern at scale: hybrid

- **Deterministic engine for the bulk, LLM for the residual.** Recipes/codemods execute mechanical transformations reproducibly; the model is called only where developer-like adaptation is needed (diagnosing the build failure the recipe couldn't resolve). — [HatchWorks](https://hatchworks.com/blog/gendd/modernization-at-portfolio-scale/), [Deterministic Code in the Loop](https://ctoadvisor.substack.com/p/deterministic-code-in-the-loop-361), [The New Stack](https://thenewstack.io/why-ai-alone-fails-at-large-scale-code-modernization/) (**body not retrievable 2026-09-03; unverified** — the two citations beside it carry this claim on their own)
- Google's internal migrations run this shape: "The LLM role is focused on the edit generation. The parts where we need to identify locations at which to make changes, and where we need to validate that the right thing took place, are handled mostly using deterministic AST techniques". Note **where the determinism sits**: on finding the sites and on validating the result, not on making the edit. — [Google, "How is Google using AI for internal code migrations?"](https://arxiv.org/pdf/2501.06972)
  - This line previously argued that variance compounds across probabilistic decisions, and cited this paper for it in the paper's own word. **The paper uses variance for the opposite argument** — that "the contextual clues and the actual changes to be made have quite a bit of variance, and these are difficult to write out in a deterministic code transformation pass. This is exactly where modern LLMs are very effective." Nothing in it says determinism is a project-management necessity. Checked against the full text 2026-09-03.
- Process hygiene the sources converge on: **one refactoring type per commit**; refactor commits never mixed with feature/fix commits; incremental git commits with plan-level rollback; containerized/sandboxed execution.

## Prior art map (why vise's slot is open)

| Neighbor | What it is | Why it isn't vise |
|---|---|---|
| [OpenRewrite](https://github.com/openrewrite/rewrite) / Moderne | Deterministic transform engine (lossless semantic trees, recipes); Java-first, with parsers for Kotlin, Groovy, JavaScript/TypeScript, Python and C# | The *cutter*, not the net; no behavior lockfile |
| [codemod CLI](https://github.com/codemod/codemod) | Scaffolds, shares **and runs** multi-step transformations; a workflow engine for migrations across repositories | Transform authoring; no verification contract |
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
