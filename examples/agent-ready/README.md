# An agent-ready vise setup

Copy these three files, adjust the build command, and copy `AGENTS.md` from the
repository root next to them. `gitignore` here is a fragment to append to the
repository's own `.gitignore`, not a file to copy verbatim — and the `.gocache/`
line in it is load-bearing, because vise compares the whole work tree around
every judged run and an unignored build cache is a harness error. Then run the
handover test before giving an agent any work:

```sh
# 0. the mechanical half, which names its own remedies
vise doctor

# 1. the operator's cold check — nothing from your shell
env -i HOME="$HOME" PATH=/usr/bin:/bin:/usr/local/go/bin vise gate --quiet

# 2. the agent's check — one turn, before any task
#    "Run `vise gate --json` and report the exit code and verdict. Change nothing."
```

Both must say green. If they disagree, the gate means something different for
the agent than for you, and every task you assign will fail for reasons that
have nothing to do with the task.

## What each guard is for

| guard | the failure it prevents |
|---|---|
| vendored dependencies | the sandbox has no network; a fetch fails only for the agent |
| build cache under the checkout | the sandbox denies writes outside the workspace |
| that cache in `.gitignore` | vise treats a file Git neither tracks nor ignores as a stray a probe wrote |
| filtering the build's failure output | toolchain chatter differs between two runs, so a plain compile error is reported as a flake, and the contract tells the agent to stop rather than fix it |
| named toolchain | `go.mod` requiring a version triggers a download |
| quiet-on-success wrapper | sandbox warnings land in the frozen bytes and turn the gate red |
| fingerprint matching the pin | catches a real toolchain change instead of PATH ordering |
| untracked declared artifacts | vise deletes artifacts before every run and refuses tracked files |

Every row is a failure that happened here, in that order, against real agents.
