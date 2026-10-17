# keep-mirrors plugins

The curated mirror-source plugins for [keep-mirrors](https://github.com/morning-start/keep-mirrors).

Every plugin is a single Lua file under `internal/plugins/`. There is no Go
code in this branch: the software reads plugins from disk at runtime, so a
plugin change here reaches users without a new release of the binary.

The path matches the one in `master`, which keeps the two in step: bringing a
plugin change back into `master` is a single

    git checkout plugins -- internal/plugins/

## Files

| File | Source |
|---|---|
| `internal/plugins/pip.lua` | Python / pip |
| `internal/plugins/npm.lua` | Node.js / npm |
| `internal/plugins/cargo.lua` | Rust / cargo |
| `internal/plugins/go.lua` | Go |

## How these are delivered

`km plugin sync` resolves this branch to a commit SHA and downloads each
`.lua` file at that pinned SHA, then validates every one against the contract
the running binary speaks. If any file fails, nothing is written.

Pinning the SHA rather than using the branch name matters: resolving the branch
and then downloading from it would let a push in between change what is
fetched, and the fetched content would never have been the one that was
validated.

The binary also ships a snapshot of this set compiled in, so a fresh install
works offline. That snapshot is refreshed at release time; `sync` is the
catch-up path, not a prerequisite.

## Writing a plugin

The contract is documented in
[docs/plugin-contract.md](https://github.com/morning-start/keep-mirrors/blob/master/docs/plugin-contract.md).

The short version: a plugin is a Lua module returning a table with metadata
(`id`, `title`, `category`, `version`, `api`, ...) and three functions
(`status`, `plan`, optional `benchmark`).

A plugin **never writes a file**. It returns a declarative list of changes and
the Go side performs the write, taking a backup first. That is what makes a
buggy plugin unable to corrupt a config file.

Two rules that are easy to get wrong:

- The `block` name a plugin writes must match the sentinel it reads back in
  `status()`, character for character.
- Never write `%-` in a Lua pattern looking for a managed-block sentinel.
  `%-` is a lazy quantifier, not an escape, so the search silently never
  matches and `status()` reports "not configured" forever.

## For your own sources

You do not need to touch this branch. Drop a `.lua` file into

```
%APPDATA%\keep-mirrors\plugins\user\      (Windows)
~/.config/keep-mirrors/plugins/user/      (POSIX)
```

A user plugin with the same `id` replaces the official one, and `km` never
writes to that directory, so your local edits survive syncs.

## License

MIT. See [LICENSE](LICENSE).