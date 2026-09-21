package dockerexec

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/ports"
)

type Worktrees struct {
	engine *Engine
	root   string
}

func NewWorktrees(engine *Engine, root string) *Worktrees {
	return &Worktrees{engine: engine, root: root}
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func (w *Worktrees) paths(id string) (ports.Worktree, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.Version() != 7 || parsed.String() != id || !filepath.IsAbs(w.root) || filepath.Clean(w.root) == "/" || strings.Contains(w.root, ":") {
		return ports.Worktree{}, domain.ErrUnavailable
	}
	base := filepath.Join(w.root, id)
	return ports.Worktree{Root: filepath.Join(base, "tree"), GitDirectory: filepath.Join(base, "repository.git")}, nil
}

// Source is mounted read-only only during preparation. A local no-hardlink
// clone owns its metadata and objects; the child's commands never mount source.
// Status uses the new private config/index, so project-configured hooks,
// fsmonitor, filters or aliases cannot execute while checking the baseline.
const prepareWorktree = `set -euo pipefail
test -d /source/.git
git clone --local --no-hardlinks --bare /source /git >/dev/null 2>&1
git --git-dir=/git config core.hooksPath /dev/null
export GIT_INDEX_FILE=/tmp/source-index
git --git-dir=/git read-tree HEAD
test -z "$(git --git-dir=/git --work-tree=/source status --porcelain=v1 --untracked-files=all --ignore-submodules=none)"
unset GIT_INDEX_FILE
test -z "$(git --git-dir=/git ls-tree -r HEAD | awk '$1 == "160000" {print $0}')"
git --git-dir=/git worktree add --detach /workspace HEAD >/dev/null 2>&1
git --git-dir=/git rev-parse HEAD`

func (w *Worktrees) Prepare(ctx context.Context, source string, op domain.Operation) (ports.Worktree, error) {
	tree, err := w.paths(op.DelegationID)
	if err != nil {
		return tree, err
	}
	if w.engine == nil || strings.Contains(source, ":") {
		return tree, domain.ErrUnavailable
	}
	if err := os.MkdirAll(w.root, 0700); err != nil {
		return tree, domain.ErrUnavailable
	}
	if err := os.Mkdir(filepath.Dir(tree.Root), 0700); err != nil {
		return tree, domain.ErrUnknownOutcome
	}
	for _, dir := range []string{tree.Root, tree.GitDirectory} {
		if err := os.Mkdir(dir, 0700); err != nil {
			return tree, domain.ErrUnavailable
		}
	}
	op.Arguments = map[string]any{"command": prepareWorktree, "timeoutMs": float64(120000)}
	result, err := w.engine.execute(ctx, false, op, []string{source + ":/source:ro", tree.Root + ":/workspace:rw", tree.GitDirectory + ":/git:rw"})
	if err != nil {
		return tree, err
	}
	output, ok := successfulOutput(result)
	if !ok || !commitPattern.MatchString(strings.TrimSpace(output)) {
		return tree, domain.ErrUnknownOutcome
	}
	tree.BaseCommit = strings.TrimSpace(output)
	return tree, nil
}

func (w *Worktrees) Open(_ context.Context, d domain.Delegation) (ports.Worktree, error) {
	tree, err := w.paths(d.ID)
	if err != nil {
		return tree, err
	}
	for _, dir := range []string{w.root, filepath.Dir(tree.Root), tree.Root, tree.GitDirectory} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return tree, domain.ErrUnknownOutcome
		}
	}
	if !commitPattern.MatchString(d.BaseCommit) {
		return tree, domain.ErrUnknownOutcome
	}
	tree.BaseCommit = d.BaseCommit
	return tree, nil
}

func (w *Worktrees) Diff(ctx context.Context, tree ports.Worktree, op domain.Operation) (domain.Result, error) {
	if !commitPattern.MatchString(tree.BaseCommit) {
		return nil, domain.ErrInvalid
	}
	op.GitDirectory = tree.GitDirectory
	// Include new files without creating a commit or applying anything to parent.
	op.Arguments = map[string]any{"command": "git add -N -- . && git diff --no-ext-diff --no-textconv --binary " + tree.BaseCommit + " -- .", "timeoutMs": float64(30000)}
	result, err := w.engine.Execute(ctx, tree.Root, false, op)
	if err != nil {
		return nil, err
	}
	output, ok := successfulOutput(result)
	if !ok {
		return nil, domain.ErrUnknownOutcome
	}
	return domain.Result{"diff": output, "baseCommit": tree.BaseCommit, "worktreeId": op.DelegationID}, nil
}

func successfulOutput(result domain.Result) (string, bool) {
	if result["exitCode"] != 0 || result["aborted"] == true || result["timedOut"] == true {
		return "", false
	}
	out, ok := result["stdout"].(map[string]any)
	if !ok || out["truncated"] == true {
		return "", false
	}
	value, ok := out["text"].(string)
	return value, ok
}
