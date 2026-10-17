// Package syncs pulls the curated plugin set from the project's plugin branch.
//
// The branch is the authoritative source for plugins: it evolves on its own
// schedule, and the binary's embedded snapshot is only what was current when
// that release was cut. Syncing lets a running binary catch up without waiting
// for a new release.
//
// The design leans on two free transports rather than the GitHub REST API:
//
//	git ls-remote            resolve a branch to a commit SHA
//	raw.githubusercontent    fetch one file at a pinned SHA
//
// Neither needs a token and neither counts against the 60-requests-per-hour
// anonymous quota, so the command works for every user out of the box. A token
// would only raise a limit that this design never approaches.
package syncs
