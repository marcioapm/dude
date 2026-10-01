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
	"fmt"
	"regexp"
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
// OpenCode reads, and the agent user as the image's user.
func FinishContainerfile(userRef, layer string) string {
	return fmt.Sprintf(`FROM %s
COPY --from=%s /rootfs/ /
RUN ["/bin/sh", "%s"]
ENV OPENCODE_CONFIG=%s DISABLE_AUTOUPDATER=1 OPENCODE_DISABLE_AUTOUPDATE=1
USER agent
WORKDIR /home/agent
`, userRef, layer, LayerSetup, LayerConfig)
}

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

// Repository is a ref without its tag or digest: where its tags live.
func Repository(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref
}

// imageWord is `image:<name>` where a FROM or --from names it: after
// whitespace or `=`, and ending the word.
var imageWord = regexp.MustCompile(`(^|[\s=])image:([a-z0-9][a-z0-9-]{0,62})(\s|$)`)

// Substitute replaces each `image:<name>` with the ref its parent's
// published user image has. A name it lacks is left as written (the
// caller resolved every parent first).
func Substitute(containerfile string, refs map[string]string) string {
	var out strings.Builder
	for _, line := range strings.SplitAfter(containerfile, "\n") {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)
		// Only instructions that take an image: FROM, and COPY/ADD --from=.
		// A RUN that echoes "image:x" is left alone. A continuation line of
		// a FROM is not a thing in practice.
		if !strings.HasPrefix(upper, "FROM ") && !strings.Contains(line, "--from=image:") {
			out.WriteString(line)
			continue
		}
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
		out.WriteString(line)
	}
	return out.String()
}

// Tail keeps the last Max bytes written to it, cut at a line start where
// it can be: a build's log, which can be far longer than anyone reads.
type Tail struct {
	Max int
	buf []byte
	cut bool
}

func (t *Tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.Max; t.Max > 0 && over > 0 {
		start := over
		if nl := strings.IndexByte(string(t.buf[start:]), '\n'); nl >= 0 && nl < 4096 {
			start += nl + 1
		}
		// Never mid-rune: step past UTF-8 continuation bytes.
		for start < len(t.buf) && t.buf[start]&0xC0 == 0x80 {
			start++
		}
		t.buf = append([]byte(nil), t.buf[start:]...)
		t.cut = true
	}
	return len(p), nil
}

// String is what is kept, with a first line saying the start is gone.
func (t *Tail) String() string {
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

// HumanMemory is podman's memory notation as people say it: 1536m → 1.5 GB.
func HumanMemory(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	var n float64
	var unit string
	if _, err := fmt.Sscanf(m, "%f%s", &n, &unit); err != nil {
		if _, err := fmt.Sscanf(m, "%f", &n); err != nil {
			return m
		}
	}
	switch unit {
	case "g":
		return trimFloat(n) + " GB"
	case "m":
		if n >= 1024 {
			return trimFloat(n/1024) + " GB"
		}
		return trimFloat(n) + " MB"
	}
	return m
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.1f", f)
	return strings.TrimSuffix(s, ".0")
}
