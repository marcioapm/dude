package phases

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// accountingScript emits exact first-parent change evidence. Only a
// two-parent merge whose second parent is contained in the recorded base
// contributes its remerge diff; every other merge is counted in full.
const accountingScript = `set -eu
repo=$1 start=$2 head=$3 base=$4
git -C "$repo" cat-file -e "$start^{commit}"
git -C "$repo" cat-file -e "$head^{commit}"
git -C "$repo" cat-file -e "$base^{commit}"
git -C "$repo" merge-base --is-ancestor "$start" "$head"
printf 'DUDE_ACCOUNTING\t%s\t%s\t%s\n' "$start" "$head" "$base"
for c in $(git -C "$repo" rev-list --first-parent --reverse "$start..$head"); do
 p=$(git -C "$repo" rev-list --parents -n1 "$c")
 set -- $p
	if [ $# -eq 3 ] && git -C "$repo" merge-base --is-ancestor "$3" "$base"; then
  printf 'DUDE_COMMIT\tintegration\t%s\n' "$c"
  git -C "$repo" show --format= --remerge-diff -M --raw --numstat "$c"
 elif [ $# -eq 2 ] || [ $# -eq 3 ]; then
  printf 'DUDE_COMMIT\tcommit\t%s\n' "$c"
  git -C "$repo" diff -M --raw --numstat "$2" "$c"
 else
  printf 'unsupported octopus merge %s\n' "$c" >&2
  exit 3
 fi
done
printf 'DUDE_END\t%s\n' "$head"
`

// publicationAccounting reads exact task-change evidence from the pushed
// checkout. Any missing header, end marker or unreadable record is an error.
func publicationAccounting(ctx context.Context, l lux.Client, runID, start, head, base, repo string) ([]string, int, error) {
	if start == "" || head == "" || base == "" {
		return nil, 0, fmt.Errorf("publication accounting needs exact start, head and base")
	}
	res, err := l.Exec(ctx, runID, []string{"sh", "-c", accountingScript, "account", "/workspace/repos/" + lux.SpecName(repo), start, head, base})
	if err != nil {
		return nil, 0, err
	}
	if res.ExitCode != 0 {
		return nil, 0, fmt.Errorf("publication accounting exited %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return parseAccounting(string(res.Stdout), start, head, base)
}

func parseAccounting(text, start, head, base string) ([]string, int, error) {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) < 2 || lines[0] != "DUDE_ACCOUNTING\t"+start+"\t"+head+"\t"+base || lines[len(lines)-1] != "DUDE_END\t"+head {
		return nil, 0, fmt.Errorf("publication accounting evidence is incomplete")
	}
	seen := map[string]bool{}
	total := 0
	add := func(path string) {
		if path != "" && !seen[path] {
			seen[path] = true
		}
	}
	for _, line := range lines[1 : len(lines)-1] {
		if line == "" || strings.HasPrefix(line, "DUDE_COMMIT\t") {
			continue
		}
		if strings.HasPrefix(line, ":") {
			tab := strings.IndexByte(line, '\t')
			if tab < 0 {
				return nil, 0, fmt.Errorf("publication accounting returned an unreadable raw line")
			}
			for _, path := range strings.Split(line[tab+1:], "\t") {
				add(path)
			}
			continue
		}
		p := strings.SplitN(line, "\t", 3)
		if len(p) != 3 {
			return nil, 0, fmt.Errorf("publication accounting returned an unreadable numstat line")
		}
		for _, n := range p[:2] {
			if n == "-" {
				continue
			}
			v, err := strconv.Atoi(n)
			if err != nil {
				return nil, 0, fmt.Errorf("publication accounting returned an unreadable count")
			}
			total += v
		}
		path := p[2]
		if before, after, ok := strings.Cut(path, " => "); ok && !strings.Contains(path, "{") {
			add(before)
			path = after
		}
		add(path)
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return paths, total, nil
}
