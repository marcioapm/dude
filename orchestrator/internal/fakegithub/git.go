package fakegithub

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/marciomartins/dude/orchestrator/internal/fakegit"
)

// The repository reads and object writes the fake makes in process
// (fakegit), git where fakegit cannot answer as git would. Ref updates are
// always git's: update-ref's lock is what makes a fake's update atomic
// against a push into the same repository.

// repo opens the repository for one call; nil when fakegit cannot read it.
func (s *Server) repo() *fakegit.Repo {
	r, ok := fakegit.Open(s.Repo)
	if !ok {
		return nil
	}
	return r
}

// email is a person's address in the fake: their first name's, lowered.
func email(name string) string {
	return strings.ToLower(strings.Fields(name)[0]) + "@example.com"
}

// commitTree is `git commit-tree tree -p parent… -m message` as name.
func (s *Server) commitTree(name, tree, message string, parents ...string) (string, error) {
	if r := s.repo(); r != nil {
		h, err := r.CommitTree(tree, parents, name, email(name), message)
		r.Close()
		if err == nil {
			return h.String(), nil
		}
	}
	args := []string{"commit-tree", tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	return s.gitAs(name, append(args, "-m", message)...)
}

// commitOf is the commit name names, as a full sha, in process; "" when
// fakegit cannot say.
func (s *Server) commitOf(r *fakegit.Repo, name string) string {
	if r == nil {
		return ""
	}
	c, err := r.Commit(name)
	if err != nil {
		return ""
	}
	return c.Hash.String()
}

// memo holds what git printed for commands whose answer is a function of
// the commits named alone: a three-way merge's tree, a diff. Keyed by the
// full shas git was given; never a command that failed outright.
type memo struct {
	mu sync.Mutex
	m  map[string]memoed
}

type memoed struct {
	out string
	// git's exit status: 0, or 1 for merge-tree's conflicts.
	status int
}

func (m *memo) get(key string) (memoed, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.m[key]
	return v, ok
}

func (m *memo) put(key string, v memoed) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]memoed{}
	}
	m.m[key] = v
}

// treeOf is `git rev-parse name^{tree}`.
func (s *Server) treeOf(name string) (string, error) {
	if r := s.repo(); r != nil {
		h, err := r.TreeOf(name)
		r.Close()
		if err == nil {
			return h.String(), nil
		}
	}
	return s.git("rev-parse", name+"^{tree}")
}

// authorOf is `git log -1 --format=%an name`: its commit's author's name.
func (s *Server) authorOf(name string) (string, error) {
	if r := s.repo(); r != nil {
		c, err := r.Commit(name)
		r.Close()
		if err == nil {
			return strings.TrimSpace(c.Author.Name), nil
		}
	}
	return s.git("log", "-1", "--format=%an", name)
}

// isAncestor is `git merge-base --is-ancestor a b` succeeding.
func (s *Server) isAncestor(a, b string) bool {
	if r := s.repo(); r != nil {
		ok, err := r.IsAncestor(a, b)
		r.Close()
		if err == nil {
			return ok
		}
	}
	_, err := s.git("merge-base", "--is-ancestor", a, b)
	return err == nil
}

// count is `git rev-list --count from..to`; "" when git fails.
func (s *Server) count(from, to string) string {
	if r := s.repo(); r != nil {
		n, err := r.Count(from, to)
		r.Close()
		if err == nil {
			return strconv.Itoa(n)
		}
	}
	out, _ := s.git("rev-list", "--count", from+".."+to)
	return out
}

// revList is `git rev-list --reverse --parents from..to`: a line per
// commit, its sha then its parents'.
func (s *Server) revList(from, to string) string {
	if r := s.repo(); r != nil {
		cs, err := r.Range(from, to)
		r.Close()
		if err == nil {
			lines := make([]string, 0, len(cs))
			for _, c := range cs {
				line := c.Hash.String()
				for _, p := range c.ParentHashes {
					line += " " + p.String()
				}
				lines = append(lines, line)
			}
			return strings.Join(lines, "\n")
		}
	}
	out, _ := s.git("rev-list", "--reverse", "--parents", from+".."+to)
	return out
}

// errConflict is merge-tree's exit 1, answered from the memo.
var errConflict = errors.New("exit status 1")

// mergeTree is `git merge-tree --write-tree a b`: the merged tree (and,
// conflicted, git's conflict report), and an error when it does not merge
// cleanly. Its answer for two commits never changes, so it is git's once
// per pair. Git is given the commits' shas: a conflict marker carries them
// rather than the names a caller used, which no caller reads.
func (s *Server) mergeTree(a, b string) (string, error) {
	r := s.repo()
	ha, hb := s.commitOf(r, a), s.commitOf(r, b)
	if r != nil {
		r.Close()
	}
	if ha == "" || hb == "" {
		return s.git("merge-tree", "--write-tree", a, b)
	}
	key := "merge-tree\x00" + ha + "\x00" + hb
	if v, ok := s.memo.get(key); ok {
		if v.status != 0 {
			return v.out, errConflict
		}
		return v.out, nil
	}
	out, err := s.git("merge-tree", "--write-tree", ha, hb)
	var exit *exec.ExitError
	switch {
	case err == nil:
		s.memo.put(key, memoed{out: out})
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		s.memo.put(key, memoed{out: out, status: 1})
	}
	return out, err
}

// diff is `git diff <args> base...head`, once per pair of commits. A
// failure is git's own on the names as given, its message naming them.
func (s *Server) diff(r *fakegit.Repo, base, head string, args ...string) (string, error) {
	named := append(append([]string{"diff"}, args...), base+"..."+head)
	hb, hh := s.commitOf(r, base), s.commitOf(r, head)
	if hb == "" || hh == "" {
		return s.git(named...)
	}
	key := "diff\x00" + strings.Join(args, "\x00") + "\x00" + hb + "\x00" + hh
	if v, ok := s.memo.get(key); ok {
		return v.out, nil
	}
	out, err := s.git(append(append([]string{"diff"}, args...), hb+"..."+hh)...)
	if err != nil {
		return s.git(named...)
	}
	s.memo.put(key, memoed{out: out})
	return out, nil
}
