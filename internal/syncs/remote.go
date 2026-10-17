package syncs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// httpTimeout bounds each individual request. Plugin files are a few kilobytes,
// so anything slower than this is a network problem worth reporting rather than
// waiting out.
const httpTimeout = 20 * time.Second

// lsRemoteTimeout bounds the branch resolution. It shells out to git, which can
// prompt for credentials on a private repo; the timeout keeps a stuck
// credential prompt from hanging the command.
const lsRemoteTimeout = 20 * time.Second

// remote is the set of transport operations sync needs. It is an interface so
// tests can supply a fake without any network access at all.
type remote interface {
	// BranchSHA resolves a branch to its current commit SHA.
	BranchSHA(ctx context.Context, repo, branch string) (string, error)
	// File returns the content of path at a pinned commit.
	File(ctx context.Context, repo, sha, path string) ([]byte, error)
}

// shaRe matches a full git object name. Anything else is rejected before it is
// interpolated into a URL, so a malformed or hostile value cannot redirect the
// download somewhere else.
var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidSHA reports whether s is a full lowercase hex object name.
func ValidSHA(s string) bool { return shaRe.MatchString(s) }

// gitRemote implements remote with git and plain HTTPS.
//
// Deliberately not the REST API: /repos/{owner}/{repo}/branches costs a request
// against the anonymous 60/hour quota, which a background sync could exhaust on
// its own. git ls-remote and raw.githubusercontent are both free.
type gitRemote struct {
	client *http.Client
	// gitPath is the git executable, resolved once.
	gitPath string
}

func newGitRemote() *gitRemote {
	return &gitRemote{
		client:  &http.Client{Timeout: httpTimeout},
		gitPath: "git",
	}
}

// BranchSHA resolves a branch name to a commit SHA via git ls-remote.
//
// ls-remote is a read-only operation that speaks the git wire protocol, so it
// works against public repositories without a token and without consuming an
// API quota.
func (r *gitRemote) BranchSHA(ctx context.Context, repo, branch string) (string, error) {
	if repo == "" || branch == "" {
		return "", fmt.Errorf("repo and branch are required")
	}
	ctx, cancel := context.WithTimeout(ctx, lsRemoteTimeout)
	defer cancel()

	url := "https://github.com/" + repo + ".git"
	cmd := exec.CommandContext(ctx, r.gitPath, "ls-remote", "--heads", url, branch)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("resolving %s/%s timed out", repo, branch)
		}
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("resolve %s/%s: %s", repo, branch, msg)
	}

	// Output is "<sha>\trefs/heads/<branch>" per line, possibly several.
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == "refs/heads/"+branch && ValidSHA(fields[0]) {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("branch %s not found in %s", branch, repo)
}

// File fetches one file at a pinned commit from raw.githubusercontent.
//
// Pinning the SHA rather than using the branch name is what makes the download
// consistent with the SHA the caller just compared: a push landing between the
// resolve and the fetch cannot change what we get.
func (r *gitRemote) File(ctx context.Context, repo, sha, path string) ([]byte, error) {
	if !ValidSHA(sha) {
		return nil, fmt.Errorf("refusing to fetch at non-SHA ref %q", sha)
	}
	if repo == "" || path == "" {
		return nil, fmt.Errorf("repo and path are required")
	}

	url := "https://raw.githubusercontent.com/" + repo + "/" + sha + "/" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "keep-mirrors-plugin-sync")

	resp, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("fetch %s timed out", path)
		}
		return nil, fmt.Errorf("fetch %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %d", path, resp.StatusCode)
	}
	// Plugin sources are a few kilobytes; the cap stops a wrong URL or a
	// hostile mirror from streaming something enormous into memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return body, nil
}
