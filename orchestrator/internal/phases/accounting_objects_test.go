package phases

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The checkout's objects and commit-graph are the agent's to rewrite after
// lux pushed: the evidence is read from a copy whose every object was
// hashed again, with no commit-graph, or not at all.
func TestAccountingEvidenceTrustsNoObjectOfTheCheckout(t *testing.T) {
	big := lines(500, "mine")
	t.Run("a forged commit-graph hides nothing", func(t *testing.T) {
		a := newAccountingRun(t)
		a.advanceBase(map[string]string{"base.go": "base\n"})
		a.syncBase()
		bigCommit := a.commit(map[string]string{"big.txt": big}, "big")
		a.commit(map[string]string{"small.txt": "x\n"}, "small")
		a.push()
		forgeSecondParent(t, a.work, a.base, bigCommit)
		if got := git(t, a.work, "rev-list", "--parents", "-1", a.base); !strings.Contains(got, bigCommit) {
			t.Fatalf("the forged graph is not read by the checkout's git: %s", got)
		}
		ev := a.account()
		samePaths(t, ev, "big.txt", "small.txt")
		if ev.Lines != 501 {
			t.Fatalf("evidence lines %d, want 501", ev.Lines)
		}
		if acc := a.published(); acc.Lines != 501 {
			t.Fatalf("published lines %d, want 501", acc.Lines)
		}
	})
	t.Run("a forged tree object is no evidence", func(t *testing.T) {
		a := newAccountingRun(t)
		a.commit(map[string]string{"big.txt": big}, "big")
		a.push()
		forgeTree(t, a.work, a.head(), a.start)
		if got := git(t, a.work, "diff", "--name-only", a.start, a.head()); got != "" {
			t.Fatalf("the forged tree is not read by the checkout's git: %q", got)
		}
		if _, err := publicationAccounting(context.Background(), a.client, a.luxID, a.start, a.head(), "", "target"); err == nil {
			t.Fatal("evidence from a forged tree object was accepted")
		}
	})
	t.Run("evidence that cannot be had counts GitHub's numbers in full", func(t *testing.T) {
		a := newAccountingRun(t)
		a.commit(map[string]string{"shared.txt": "task\n", "mine.go": big}, "task")
		a.advanceBase(map[string]string{"shared.txt": "base\n"})
		a.syncBase()
		_, _ = exec.Command("git", "-C", a.work, "merge", "-q", "--no-edit", "lux/main").CombinedOutput()
		write(t, filepath.Join(a.work, "shared.txt"), "task\nbase\n")
		git(t, a.work, "commit", "-qam", "merge")
		a.push()
		forgeTree(t, a.work, a.head(), a.start)
		acc := a.published()
		if !strings.Contains(acc.Full, "evidence is unavailable") || acc.Lines < 500 {
			t.Fatalf("lines %d full %q, want GitHub's whole count", acc.Lines, acc.Full)
		}
	})
}

// forgeSecondParent rewrites the checkout's commit-graph so that commit
// reads as a merge whose second parent is extra, as an agent could: the
// file is the checkout's, and git trusts it without checking its hash.
func forgeSecondParent(t *testing.T, work, commit, extra string) {
	t.Helper()
	git(t, work, "-c", "commitGraph.generationVersion=1", "commit-graph", "write", "--reachable", "--no-progress")
	path := filepath.Join(work, ".git", "objects", "info", "commit-graph")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	chunks := map[string]int{}
	for i := range int(data[6]) {
		e := data[8+12*i:]
		chunks[string(e[:4])] = int(binary.BigEndian.Uint64(e[4:12]))
	}
	n := int(binary.BigEndian.Uint32(data[chunks["OIDF"]+255*4:]))
	index := func(sha string) int {
		raw, _ := hex.DecodeString(sha)
		for i := range n {
			if bytes.Equal(data[chunks["OIDL"]+20*i:][:20], raw) {
				return i
			}
		}
		t.Fatalf("%s is not in the commit-graph", sha)
		return 0
	}
	c, x := index(commit), index(extra)
	entry := data[chunks["CDAT"]+36*c:]
	binary.BigEndian.PutUint32(entry[24:], uint32(x))
	// A generation above both parents', so walks do not stop early.
	word := binary.BigEndian.Uint32(entry[28:])
	binary.BigEndian.PutUint32(entry[28:], word+(16<<2))
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// forgeTree rewrites commit's loose tree object with the bytes of from's
// tree: git checks a commit's hash as it reads it, not a tree's.
func forgeTree(t *testing.T, work, commit, from string) {
	t.Helper()
	tree := git(t, work, "rev-parse", commit+"^{tree}")
	raw, err := exec.Command("git", "-C", work, "cat-file", "tree", from+"^{tree}").Output()
	if err != nil {
		t.Fatal(err)
	}
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	_, _ = w.Write(append([]byte("tree "+strconv.Itoa(len(raw))+"\x00"), raw...))
	_ = w.Close()
	path := filepath.Join(work, ".git", "objects", tree[:2], tree[2:])
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("%s is not a loose object: %v", tree, err)
	}
	if err := os.WriteFile(path, z.Bytes(), 0o444); err != nil {
		t.Fatal(err)
	}
}
