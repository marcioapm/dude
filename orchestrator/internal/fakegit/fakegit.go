// Package fakegit answers, in process, the plain git reads and object
// writes the test fakes (fakegithub, fakelux) make on a repository: name
// resolution as rev-parse does it, ancestry, commit ranges, and writing
// blobs, trees and commits. Ref updates are not here: those stay with git,
// whose lock files are what keep a fake's update atomic against a git push
// into the same repository.
//
// Every answer is git's or none: ok false (or ErrUnknown) where the case is
// one this package does not decide exactly as git would — an abbreviated
// sha, revision syntax, a repository format it does not read — and the
// caller then runs git as before.
package fakegit

import (
	"container/heap"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

var (
	// ErrUnknown: this package cannot answer as git would; run git.
	ErrUnknown = errors.New("fakegit: not answered in process")
	// ErrNotFound: git would find no object by this name either.
	ErrNotFound = errors.New("fakegit: no such name")
)

// Repo is one repository opened for one call: its pack list is read when
// opened, so a handle kept across a push would miss the push's pack.
type Repo struct {
	st *filesystem.Storage
}

// Open opens the repository whose git directory is dir (a bare
// repository, or a checkout's .git); ok false when dir is not one (a
// checkout's top, a .git file, a linked worktree's, one with alternates) or
// its format is one git must read (an extension such as reftable or
// sha256, or format version 1).
func Open(dir string) (*Repo, bool) {
	for _, p := range []string{"HEAD", "config", "objects", "refs"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			return nil, false
		}
	}
	// A linked worktree's git directory keeps its refs elsewhere; an
	// alternate's objects are another repository's.
	for _, p := range []string{"commondir", "objects/info/alternates"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err == nil {
			return nil, false
		}
	}
	st := filesystem.NewStorageWithOptions(osfs.New(dir), cache.NewObjectLRUDefault(), filesystem.Options{})
	cfg, err := st.Config()
	if err != nil {
		_ = st.Close()
		return nil, false
	}
	v := cfg.Raw.Section("core").Option("repositoryformatversion")
	if v != "" && v != "0" || len(cfg.Raw.Section("extensions").Options) > 0 {
		_ = st.Close()
		return nil, false
	}
	return &Repo{st: st}, true
}

func (r *Repo) Close() { _ = r.st.Close() }

// Names this package resolves itself: a ref path or branch name of plain
// characters. Revision syntax (^ ~ : @{ ..), spaces and the like go to git.
var plainName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)

var (
	fullHex  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	someHex  = regexp.MustCompile(`^[0-9a-fA-F]{4,}$`)
	describe = regexp.MustCompile(`-g[0-9a-fA-F]{4,}$`)
	rootRef  = regexp.MustCompile(`^[A-Z_-]+$`)
)

// Resolve is `git rev-parse --verify <name>`: the object name names,
// unpeeled. A full lowercase sha is itself, existing or not, as git takes
// it; any other name is looked up as a ref by git's rules (as given, then
// under refs/, refs/tags/, refs/heads/, refs/remotes/, refs/remotes/…/HEAD),
// the first match winning. ErrNotFound only where git, failing those, would
// not go on to read the name as an abbreviated sha.
func (r *Repo) Resolve(name string) (plumbing.Hash, error) {
	if fullHex.MatchString(name) {
		return plumbing.NewHash(name), nil
	}
	if !plainName.MatchString(name) || strings.Contains(name, "..") || strings.HasSuffix(name, ".lock") ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.Contains(name, "//") {
		return plumbing.ZeroHash, ErrUnknown
	}
	for i, rule := range plumbing.RefRevParseRules {
		full := strings.ReplaceAll(rule, "%s", name)
		// As git: the name as given is a ref only under refs/ or as a
		// root ref (HEAD, FETCH_HEAD); "config" is not one.
		if i == 0 && !strings.HasPrefix(full, "refs/") && !rootRef.MatchString(full) {
			continue
		}
		h, err := r.ref(plumbing.ReferenceName(full))
		switch {
		case err == nil:
			return h, nil
		case !errors.Is(err, ErrNotFound):
			return plumbing.ZeroHash, err
		}
	}
	if someHex.MatchString(name) || describe.MatchString(name) {
		return plumbing.ZeroHash, ErrUnknown
	}
	return plumbing.ZeroHash, ErrNotFound
}

// ref is the object a ref points at, following symbolic refs; ErrNotFound
// for a ref that is not there or whose symbolic chain ends nowhere.
func (r *Repo) ref(name plumbing.ReferenceName) (plumbing.Hash, error) {
	for range 6 {
		ref, err := r.st.Reference(name)
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return plumbing.ZeroHash, ErrNotFound
		}
		if err != nil {
			return plumbing.ZeroHash, ErrUnknown
		}
		if ref.Type() == plumbing.HashReference {
			return ref.Hash(), nil
		}
		name = ref.Target()
	}
	return plumbing.ZeroHash, ErrUnknown
}

// Commit is the commit name names, a tag peeled; ErrUnknown for anything
// else (git's error is then git's to give).
func (r *Repo) Commit(name string) (*object.Commit, error) {
	h, err := r.Resolve(name)
	if err != nil {
		return nil, err
	}
	return r.peelCommit(h)
}

func (r *Repo) peelCommit(h plumbing.Hash) (*object.Commit, error) {
	o, err := r.peel(h, plumbing.CommitObject)
	if err != nil {
		return nil, err
	}
	c, err := object.DecodeCommit(r.st, o)
	if err != nil {
		return nil, ErrUnknown
	}
	return c, nil
}

// tree is the tree name names: a tree, or a commit's (a tag peeled).
func (r *Repo) tree(name string) (*object.Tree, error) {
	h, err := r.Resolve(name)
	if err != nil {
		return nil, err
	}
	o, err := r.peel(h, plumbing.TreeObject)
	if err != nil {
		return nil, err
	}
	t, err := object.DecodeTree(r.st, o)
	if err != nil {
		return nil, ErrUnknown
	}
	return t, nil
}

// peel follows tags from h, and a commit to its tree when want is a tree,
// to an object of type want; ErrUnknown for any other object on the way.
func (r *Repo) peel(h plumbing.Hash, want plumbing.ObjectType) (plumbing.EncodedObject, error) {
	for range 10 {
		o, err := r.st.EncodedObject(plumbing.AnyObject, h)
		if err != nil {
			return nil, ErrUnknown
		}
		switch t := o.Type(); {
		case t == want:
			return o, nil
		case t == plumbing.CommitObject && want == plumbing.TreeObject:
			c, err := object.DecodeCommit(r.st, o)
			if err != nil {
				return nil, ErrUnknown
			}
			h = c.TreeHash
		case t == plumbing.TagObject:
			tag, err := object.DecodeTag(r.st, o)
			if err != nil {
				return nil, ErrUnknown
			}
			h = tag.Target
		default:
			return nil, ErrUnknown
		}
	}
	return nil, ErrUnknown
}

// TreeOf is `git rev-parse <name>^{tree}`.
func (r *Repo) TreeOf(name string) (plumbing.Hash, error) {
	t, err := r.tree(name)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return t.Hash, nil
}

// ancestors is every commit reachable from tip, tip included.
func (r *Repo) ancestors(tip *object.Commit) (map[plumbing.Hash]*object.Commit, error) {
	seen := map[plumbing.Hash]*object.Commit{tip.Hash: tip}
	for todo := []*object.Commit{tip}; len(todo) > 0; {
		c := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		for _, p := range c.ParentHashes {
			if seen[p] != nil {
				continue
			}
			pc, err := r.peelCommit(p)
			if err != nil {
				return nil, err
			}
			seen[p] = pc
			todo = append(todo, pc)
		}
	}
	return seen, nil
}

// IsAncestor is `git merge-base --is-ancestor a b`: a is b or one of its
// ancestors.
func (r *Repo) IsAncestor(a, b string) (bool, error) {
	ca, err := r.Commit(a)
	if err != nil {
		return false, err
	}
	cb, err := r.Commit(b)
	if err != nil {
		return false, err
	}
	all, err := r.ancestors(cb)
	if err != nil {
		return false, err
	}
	return all[ca.Hash] != nil, nil
}

// Range is `git rev-list --reverse from..to`: the commits to has and from
// lacks, in git's order reversed. Git lists them newest committer date
// first, a tie in the order they were reached (each commit's parents in
// order); this walk reaches them as git's does, from to alone, as git's
// walk from from reaches none of them.
func (r *Repo) Range(from, to string) ([]*object.Commit, error) {
	cf, err := r.Commit(from)
	if err != nil {
		return nil, err
	}
	ct, err := r.Commit(to)
	if err != nil {
		return nil, err
	}
	excluded, err := r.ancestors(cf)
	if err != nil {
		return nil, err
	}
	var out []*object.Commit
	if excluded[ct.Hash] != nil {
		return out, nil
	}
	q := &byDate{}
	seen := map[plumbing.Hash]bool{ct.Hash: true}
	q.push(ct)
	for q.Len() > 0 {
		c := heap.Pop(q).(queued).c
		out = append(out, c)
		for _, p := range c.ParentHashes {
			if seen[p] || excluded[p] != nil {
				continue
			}
			seen[p] = true
			pc, err := r.peelCommit(p)
			if err != nil {
				return nil, err
			}
			q.push(pc)
		}
	}
	slices.Reverse(out)
	return out, nil
}

// Count is `git rev-list --count from..to`.
func (r *Repo) Count(from, to string) (int, error) {
	cs, err := r.Range(from, to)
	return len(cs), err
}

// byDate is git's commit queue: newest committer date first, ties first
// in, first out.
type byDate struct {
	items []queued
	n     int
}

type queued struct {
	c   *object.Commit
	ctr int
}

func (q *byDate) push(c *object.Commit) { q.n++; heap.Push(q, queued{c, q.n}) }
func (q *byDate) Len() int              { return len(q.items) }
func (q *byDate) Swap(i, j int)         { q.items[i], q.items[j] = q.items[j], q.items[i] }
func (q *byDate) Push(x any)            { q.items = append(q.items, x.(queued)) }
func (q *byDate) Pop() any {
	x := q.items[len(q.items)-1]
	q.items = q.items[:len(q.items)-1]
	return x
}
func (q *byDate) Less(i, j int) bool {
	a, b := q.items[i], q.items[j]
	if da, db := a.c.Committer.When.Unix(), b.c.Committer.When.Unix(); da != db {
		return da > db
	}
	return a.ctr < b.ctr
}

// HasPath is `git cat-file -e <name>:<path>`.
func (r *Repo) HasPath(name, path string) (bool, error) {
	if !validPath(path) {
		return false, ErrUnknown
	}
	t, err := r.tree(name)
	if err != nil {
		return false, err
	}
	parts := strings.Split(path, "/")
	for i, name := range parts {
		e := (&dir{tree: t}).entry(name)
		switch {
		case e == nil:
			return false, nil
		case i == len(parts)-1:
			return true, nil
		case e.Mode != filemode.Dir:
			// A path under a file (or a submodule): no such object.
			return false, nil
		}
		if t, err = object.GetTree(r.st, e.Hash); err != nil {
			return false, ErrUnknown
		}
	}
	return false, ErrUnknown
}

// WriteBlob is `git hash-object -w --stdin`: no filters, as git applies
// none to standard input.
func (r *Repo) WriteBlob(content string) (plumbing.Hash, error) {
	o := r.st.NewEncodedObject()
	o.SetType(plumbing.BlobObject)
	o.SetSize(int64(len(content)))
	w, err := o.Writer()
	if err != nil {
		return plumbing.ZeroHash, ErrUnknown
	}
	if _, err := io.WriteString(w, content); err != nil {
		return plumbing.ZeroHash, ErrUnknown
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, ErrUnknown
	}
	return r.store(o)
}

func (r *Repo) store(o plumbing.EncodedObject) (plumbing.Hash, error) {
	h, err := r.st.SetEncodedObject(o)
	if err != nil {
		return plumbing.ZeroHash, ErrUnknown
	}
	return h, nil
}

// TreeWith is the tree of treeish with files (path → content) written
// over it as regular files (100644): what `read-tree <treeish>`, then
// `update-index --add --cacheinfo 100644,<blob>,<path>` per file, then
// `write-tree` make in a scratch index. A path git would refuse, or one
// that would turn a file into a directory or back, is ErrUnknown: git's
// refusal is then git's to give.
func (r *Repo) TreeWith(treeish string, files map[string]string) (plumbing.Hash, error) {
	base, err := r.tree(treeish)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if len(files) == 0 {
		return base.Hash, nil
	}
	root := &dir{tree: base, sub: map[string]*dir{}, blobs: map[string]plumbing.Hash{}}
	for path, content := range files {
		if !validPath(path) {
			return plumbing.ZeroHash, ErrUnknown
		}
		blob, err := r.WriteBlob(content)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		parts := strings.Split(path, "/")
		d := root
		for _, name := range parts[:len(parts)-1] {
			if d, err = r.subdir(d, name); err != nil {
				return plumbing.ZeroHash, err
			}
		}
		name := parts[len(parts)-1]
		if e := d.entry(name); e != nil && e.Mode == filemode.Dir || d.sub[name] != nil {
			return plumbing.ZeroHash, ErrUnknown
		}
		d.blobs[name] = blob
	}
	return r.writeDir(root)
}

// dir is a tree being changed: its entries as they were (tree, nil for a
// directory new here), subdirectories changed, files written.
type dir struct {
	tree  *object.Tree
	sub   map[string]*dir
	blobs map[string]plumbing.Hash
}

func (d *dir) entry(name string) *object.TreeEntry {
	if d.tree == nil {
		return nil
	}
	for i := range d.tree.Entries {
		if d.tree.Entries[i].Name == name {
			return &d.tree.Entries[i]
		}
	}
	return nil
}

func (r *Repo) subdir(d *dir, name string) (*dir, error) {
	if s := d.sub[name]; s != nil {
		return s, nil
	}
	if _, ok := d.blobs[name]; ok {
		return nil, ErrUnknown
	}
	s := &dir{sub: map[string]*dir{}, blobs: map[string]plumbing.Hash{}}
	if e := d.entry(name); e != nil {
		if e.Mode != filemode.Dir {
			return nil, ErrUnknown
		}
		t, err := object.GetTree(r.st, e.Hash)
		if err != nil {
			return nil, ErrUnknown
		}
		s.tree = t
	}
	d.sub[name] = s
	return s, nil
}

func (r *Repo) writeDir(d *dir) (plumbing.Hash, error) {
	var entries []object.TreeEntry
	if d.tree != nil {
		for _, e := range d.tree.Entries {
			if _, ok := d.blobs[e.Name]; !ok && d.sub[e.Name] == nil {
				entries = append(entries, e)
			}
		}
	}
	for name, h := range d.blobs {
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: h})
	}
	for name, s := range d.sub {
		h, err := r.writeDir(s)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: h})
	}
	slices.SortFunc(entries, func(a, b object.TreeEntry) int {
		return strings.Compare(sortName(a), sortName(b))
	})
	o := r.st.NewEncodedObject()
	if err := (&object.Tree{Entries: entries}).Encode(o); err != nil {
		return plumbing.ZeroHash, ErrUnknown
	}
	return r.store(o)
}

// sortName is git's tree order key: a directory sorts as if its name ended
// in "/".
func sortName(e object.TreeEntry) string {
	if e.Mode == filemode.Dir {
		return e.Name + "/"
	}
	return e.Name
}

// validPath is a path update-index takes as it is: no empty, ".", ".." or
// ".git" component, nothing git would normalise or refuse.
func validPath(path string) bool {
	if path == "" || strings.ContainsAny(path, "\x00\\") {
		return false
	}
	for _, c := range strings.Split(path, "/") {
		if c == "" || c == "." || c == ".." || strings.EqualFold(c, ".git") {
			return false
		}
	}
	return true
}

// CommitTree is `git commit-tree <tree> -p <parent>… -m <message>` with
// author and committer both name <email>, now: parents and tree named as
// git takes them, each a commit (a tag peeled) and a tree.
func (r *Repo) CommitTree(tree string, parents []string, name, email, message string) (plumbing.Hash, error) {
	th, err := r.TreeOf(tree)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if t, _ := r.Resolve(tree); t != th {
		// A commit named as the tree: git's commit-tree peels it too, but
		// leave the case to git.
		return plumbing.ZeroHash, ErrUnknown
	}
	var ps []plumbing.Hash
	for _, p := range parents {
		c, err := r.Commit(p)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		ps = append(ps, c.Hash)
	}
	// As commit-tree's -m: the message's last line ended.
	if message != "" && !strings.HasSuffix(message, "\n") {
		message += "\n"
	}
	sig := object.Signature{Name: name, Email: email, When: time.Unix(time.Now().Unix(), 0)}
	c := &object.Commit{Author: sig, Committer: sig, Message: message, TreeHash: th, ParentHashes: ps}
	o := r.st.NewEncodedObject()
	if err := c.Encode(o); err != nil {
		return plumbing.ZeroHash, ErrUnknown
	}
	return r.store(o)
}
