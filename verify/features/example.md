# Example

- What exists: the copyable setup in [`examples/agent-ready/`](../../examples/agent-ready/) — a manifest, a probe wrapper, and an ignore fragment — instantiated into a real Go project and driven end to end: `doctor` clean but for the missing baseline, `record`, `doctor` ready, a green gate, and a red gate for a change only the declared artifact shows.
- User route: copy the three files into a project, adjust the build command, commit, record.
- Harness route: `scripts/verify verify example`.
- What usually lies: a template that has been written and never run; a manifest that satisfies its own author's machine; and a probe set that watches stdout while the thing that changed is an artifact on disk. The last one is why this feature breaks the app in a way only the artifact reveals.
