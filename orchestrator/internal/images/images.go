// Package images is dude's image library outside the backend: the builder
// that turns a queued version into a published image (dude-image-builder,
// its own process and database role), and how a Run's image is chosen and
// finished with the dude layer (the orchestrator).
//
// A version is built in two images. The user image is its Containerfile
// alone, pushed as <repository>:<version id>-user; children build FROM it,
// so the dude layer never stacks. The final image is the user image with
// the current dude layer copied on and set up (FinishContainerfile), pushed
// as <repository>:<version id>-<layer's first 12 hex>; Runs use it. A
// version is finished again, by a finish job, the first time a Run needs it
// under a dude layer it has not had.
//
// Design: docs/design/images.md.
package images

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The setup the dude layer carries, run as root in the finish build, and
// the OpenCode configuration it ships (§6 of the design: aiverse builds the
// layer; these paths are the contract).
const (
	LayerSetup  = "/usr/local/share/dude/setup.sh"
	LayerConfig = "/usr/local/share/dude/opencode.json"
)

// FinishContainerfile adds the dude layer to a built user image: its
// files, its setup (the agent user, the git identity), the environment
// OpenCode reads, and the agent user as the image's user. HOME is set
// because agent may be a second name for uid 1000 (node on node images),
// and podman takes HOME from the first passwd entry for that uid.
//
// The same step gives agent /etc/subuid and /etc/subgid entries when it has
// none (SubIDs), so every image with an engine can run containers as agent.
func FinishContainerfile(userRef, layer string) string {
	return fmt.Sprintf(`FROM %s
COPY --from=%s /rootfs/ /
RUN /bin/sh %s && %s
ENV OPENCODE_CONFIG=%s DISABLE_AUTOUPDATER=1 OPENCODE_DISABLE_AUTOUPDATE=1 HOME=/home/agent
USER agent
WORKDIR /home/agent
`, userRef, layer, LayerSetup, SubIDs, LayerConfig)
}

// SubIDs writes agent's subordinate ids where /etc/subuid or /etc/subgid
// has none: every id but 0 and agent's own below 65536. lux runs each Run
// in a user namespace of 65536 ids (--userns=auto:size=65536), so a range
// above it (useradd's 100000:65536) could not be mapped inside a Run; this
// is lux's own tests/images/nested layout. The owner is agent, or its uid
// where another name comes first for that uid (node on node images):
// podman looks the caller up by uid and matches that name or the number,
// so an agent line behind another name does not stop this one. Lines the
// image has for that other name are its own; the container check fails a
// version whose lines a Run cannot map.
const SubIDs = `u=$(id -u agent) && n=$(grep -m1 "^[^:]*:[^:]*:$u:" /etc/passwd | cut -d: -f1) && ` +
	`o=agent && { [ "$n" = agent ] || o=$u; } && for f in /etc/subuid /etc/subgid; do ` +
	`grep -qs -e "^$o:" -e "^$u:" $f || ` +
	`printf '%s:1:%d\n%s:%d:%d\n' $o $((u-1)) $o $((u+1)) $((65535-u)) >> $f; done`

// UserTag is where a version's user image is pushed.
func UserTag(repository, versionID string) string {
	return repository + ":" + versionID + "-user"
}

// FinalTag is where a version finished with layer is pushed: the layer's
// digest in the tag, so each (version, layer) has its own immutable tag.
func FinalTag(repository, versionID, layer string) string {
	return repository + ":" + versionID + "-" + LayerShort(layer)
}

// LayerShort is a layer's digest's first 12 hex, as people and tags name it.
func LayerShort(layer string) string {
	d := layer
	if i := strings.LastIndex(d, "@"); i >= 0 {
		d = d[i+1:]
	}
	d = strings.TrimPrefix(d, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}

var digestRef = regexp.MustCompile(`^[^\s@]+@sha256:[0-9a-f]{64}$`)

// IsDigestRef is a ref naming its image by digest (…@sha256:<64 hex>), as
// DUDE_LAYER_IMAGE must.
func IsDigestRef(ref string) bool { return digestRef.MatchString(ref) }

// imageWord is `image:<name>` where a FROM or --from names it: after
// whitespace or `=`, and ending the word.
var imageWord = regexp.MustCompile(`(^|[\s=])image:([a-z0-9][a-z0-9-]{0,62})(\s|$)`)

// Substitute replaces each `image:<name>` with the ref its parent's
// published user image has. A name it lacks is left as written (the
// caller resolved every parent first). Instructions are read as the lint
// reads them, `\`-continued lines joined: only one that takes an image
// (FROM, or COPY/ADD with --from=image:) is rewritten, on every one of its
// lines, so a RUN that echoes "image:x" is left alone.
func Substitute(containerfile string, refs map[string]string) string {
	var out strings.Builder
	lines := strings.SplitAfter(containerfile, "\n")
	for i := 0; i < len(lines); {
		end := i + 1
		for end < len(lines) && strings.HasSuffix(strings.TrimRight(lines[end-1], " \t\r\n"), "\\") {
			end++
		}
		group := lines[i:end]
		joined := strings.Join(group, "")
		takesImage := strings.HasPrefix(strings.ToUpper(strings.TrimSpace(joined)), "FROM ") ||
			strings.Contains(joined, "--from=image:")
		for _, line := range group {
			if takesImage {
				line = substituteLine(line, refs)
			}
			out.WriteString(line)
		}
		i = end
	}
	return out.String()
}

func substituteLine(line string, refs map[string]string) string {
	// Twice: adjacent matches share the separator between them.
	for range 2 {
		line = imageWord.ReplaceAllStringFunc(line, func(m string) string {
			sub := imageWord.FindStringSubmatch(m)
			ref, ok := refs[sub[2]]
			if !ok {
				return m
			}
			return sub[1] + ref + sub[3]
		})
	}
	return line
}

// Tail keeps the last Max bytes written to it, cut at a line start where
// it can be: a build's log, which can be far longer than anyone reads. The
// buffer is trimmed only once it holds twice Max, so the copy is amortised
// over at least Max bytes of writes.
type Tail struct {
	Max int
	buf []byte
	cut bool
}

func (t *Tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if t.Max > 0 && len(t.buf) > 2*t.Max {
		t.trim()
	}
	return len(p), nil
}

// trim drops all but the last Max bytes, from the next line start within
// 4 KiB, never mid-rune.
func (t *Tail) trim() {
	over := len(t.buf) - t.Max
	if over <= 0 {
		return
	}
	start := over
	if nl := bytes.IndexByte(t.buf[start:min(len(t.buf), start+4096)], '\n'); nl >= 0 {
		start += nl + 1
	}
	for start < len(t.buf) && t.buf[start]&0xC0 == 0x80 {
		start++
	}
	t.buf = append(t.buf[:0], t.buf[start:]...)
	t.cut = true
}

// String is what is kept, with a first line saying the start is gone.
func (t *Tail) String() string {
	t.trim()
	if t.cut {
		return "… (the start of the log is gone: dude keeps the last 1 MiB)\n" + string(t.buf)
	}
	return string(t.buf)
}

// LogMax is the most of a build's log dude keeps.
const LogMax = 1 << 20

var stepLine = regexp.MustCompile(`(?m)^STEP (\d+)/(\d+):`)

// lastStep is the step a log reached, "" before any.
func lastStep(log string) string {
	m := stepLine.FindAllStringSubmatch(log, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// Failure is why a build failed, in one sentence a person reads in the
// image's history: the reason podman's output shows, else what dude was
// doing when it failed.
func Failure(stage, log string, err error, memory string, timedOut bool) string {
	step := lastStep(log)
	at := ""
	if step != "" {
		at = " at step " + step
	}
	tail := log
	if len(tail) > 8192 {
		tail = tail[len(tail)-8192:]
	}
	switch {
	case timedOut:
		return fmt.Sprintf("took longer than the build's time limit%s", at)
	case strings.Contains(tail, "the image needs git"):
		return "the image needs git: agents commit with it"
	case stage == "finishing" && (strings.Contains(tail, `"/bin/sh": stat /bin/sh: no such file`) ||
		strings.Contains(tail, "/bin/sh: no such file or directory") ||
		strings.Contains(tail, `exec: "/bin/sh": stat`) ||
		strings.Contains(tail, "executable file `/bin/sh` not found") ||
		strings.Contains(tail, "/bin/sh: not found") || strings.Contains(tail, "error while loading shared libraries")):
		return "the dude layer needs /bin/sh and glibc in the image"
	case strings.Contains(tail, "exit status 137") || strings.Contains(tail, "signal: killed") || strings.Contains(tail, "OOMKilled") ||
		strings.Contains(tail, "Out of memory") || strings.Contains(tail, "out of memory"):
		return fmt.Sprintf("ran out of memory (%s)%s", HumanMemory(memory), at)
	case stage == "pushing" || stage == "finishing" && strings.Contains(tail, "Error: pushing"):
		return "pushing to the registry failed: " + lastLine(tail, err)
	case strings.Contains(tail, "no such host") || strings.Contains(tail, "connection refused") ||
		strings.Contains(tail, "i/o timeout") || strings.Contains(tail, "TLS handshake timeout"):
		return "a network request failed" + at + ": " + lastLine(tail, err)
	case strings.Contains(tail, "manifest unknown") || strings.Contains(tail, "not found: manifest") ||
		strings.Contains(tail, "requested access to the resource is denied") || strings.Contains(tail, "unauthorized"):
		return "a base image could not be pulled: " + lastLine(tail, err)
	}
	if step != "" {
		return fmt.Sprintf("step %s failed: %s", step, lastLine(tail, err))
	}
	return lastLine(tail, err)
}

// lastLine is podman's last error line, else err's text.
func lastLine(log string, err error) string {
	lines := strings.Split(strings.TrimSpace(log), "\n")
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-20; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "Error: ") {
			return strings.TrimPrefix(l, "Error: ")
		}
	}
	if err != nil {
		return err.Error()
	}
	return "it failed"
}

// memoryNotation is the podman memory notation dude accepts for
// DUDE_BUILDER_MEMORY: whole bytes, or whole k/m/g (KiB, MiB, GiB).
var memoryNotation = regexp.MustCompile(`^([0-9]+)([bkmg]?)$`)

// MemoryBytes is a memory setting in bytes; false when it is not in
// memoryNotation.
func MemoryBytes(m string) (int64, bool) {
	sub := memoryNotation.FindStringSubmatch(strings.ToLower(strings.TrimSpace(m)))
	if sub == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(sub[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return n << map[string]int{"": 0, "b": 0, "k": 10, "m": 20, "g": 30}[sub[2]], true
}

// HumanMemory is a memory setting as people say it: 1536m → 1.5 GB. One
// outside memoryNotation is returned as written.
func HumanMemory(m string) string {
	n, ok := MemoryBytes(m)
	switch {
	case !ok:
		return m
	case n >= 1<<30:
		return trimFloat(float64(n)/(1<<30)) + " GB"
	case n >= 1<<20:
		return trimFloat(float64(n)/(1<<20)) + " MB"
	case n >= 1<<10:
		return trimFloat(float64(n)/(1<<10)) + " KB"
	}
	return fmt.Sprintf("%d B", n)
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.1f", f)
	return strings.TrimSuffix(s, ".0")
}
