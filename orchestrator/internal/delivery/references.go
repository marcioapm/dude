package delivery

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// attachmentRef is an image a task's Markdown shows from its own uploads:
// `![name](attachment:att_…)`, the URL in angle brackets or not, with a
// title or not. packages/domain attachments.ts parses the same way, and
// the backend attaches what it finds there on every save. Blanks are ASCII
// space and tab only, and the alt holds no unescaped `[`, as there.
var attachmentRef = regexp.MustCompile(`!\[((?:\\[^\n]|[^\[\]\\\n])*)\]\([ \t]*<?attachment:(att_[A-Za-z0-9]+)>?(?:[ \t]+(?:"([^"\n]*)"|'([^'\n]*)'|\(([^)\n]*)\)))?[ \t]*\)`)

var fenceOpen = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")

var unescape = regexp.MustCompile(`\\([^\n])`)

// ImageRef is one reference in a text: what it names, its alt text, its
// title (the dialog's layout words, never sent to an agent), and the byte
// offsets of the whole `![…](…)`.
type ImageRef struct {
	ID, Alt, Title string
	From, To       int
}

// ImageRefs are the attachment references in Markdown, in order. Inside a
// fenced code block or a code span it is text, as Markdown shows it.
func ImageRefs(text string) []ImageRef {
	var out []ImageRef
	fence := ""
	offset := 0
	for _, line := range strings.Split(text, "\n") {
		var code bool
		if fence, code = fenceAfter(fence, line); !code {
			masked := maskCodeSpans(line)
			for _, m := range attachmentRef.FindAllStringSubmatchIndex(masked, -1) {
				ref := ImageRef{
					ID:   masked[m[4]:m[5]],
					Alt:  unescape.ReplaceAllString(line[m[2]:m[3]], "$1"),
					From: offset + m[0], To: offset + m[1],
				}
				for g := 6; g <= 10; g += 2 {
					if m[g] >= 0 {
						ref.Title = line[m[g]:m[g+1]]
					}
				}
				out = append(out, ref)
			}
		}
		offset += len(line) + 1
	}
	return out
}

// fenceAfter is the fence open after line, given the one open before it
// ("" for none), and whether line is code: inside a fence, or one's opener
// or closer.
func fenceAfter(fence, line string) (string, bool) {
	opener := ""
	if m := fenceOpen.FindStringSubmatch(line); m != nil {
		opener = m[1]
	}
	switch {
	case fence != "":
		if opener != "" && opener[0] == fence[0] && len(opener) >= len(fence) && strings.Trim(line, " \t") == opener {
			return "", true
		}
		return fence, true
	case opener != "":
		return opener, true
	}
	return "", false
}

// UnclosedFence is the fence text leaves open at its end, "" for none: the
// closer to write after it so that what follows is not read as code.
func UnclosedFence(text string) string {
	fence := ""
	for _, line := range strings.Split(text, "\n") {
		fence, _ = fenceAfter(fence, line)
	}
	return fence
}

// maskCodeSpans blanks each closed code span's content, keeping offsets.
func maskCodeSpans(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '`' {
			b.WriteByte(line[i])
			i++
			continue
		}
		n := 0
		for i+n < len(line) && line[i+n] == '`' {
			n++
		}
		ticks := strings.Repeat("`", n)
		closeAt := -1
		for from := i + n; from < len(line); {
			k := strings.Index(line[from:], ticks)
			if k < 0 {
				break
			}
			k += from
			if k+n < len(line) && line[k+n] == '`' {
				// A longer run of backticks does not close it.
				for k < len(line) && line[k] == '`' {
					k++
				}
				from = k
				continue
			}
			closeAt = k
			break
		}
		if closeAt < 0 {
			b.WriteString(ticks)
			i += n
			continue
		}
		b.WriteString(strings.Repeat(" ", closeAt+n-i))
		i = closeAt + n
	}
	return b.String()
}

// TaskImageIDs are the distinct ids a task's goal and criteria reference:
// the goal's, then each criterion's, in order of first appearance. This is
// the order the agent is given them in, and numbers them by.
func TaskImageIDs(goal string, criteria []string) []string {
	var ids []string
	for _, text := range append([]string{goal}, criteria...) {
		for _, r := range ImageRefs(text) {
			if !slices.Contains(ids, r.ID) {
				ids = append(ids, r.ID)
			}
		}
	}
	return ids
}

// ReplaceImageRefs rewrites each reference in text to what with returns
// for it; everything around it is left as it was.
func ReplaceImageRefs(text string, with func(ImageRef) string) string {
	refs := ImageRefs(text)
	if len(refs) == 0 {
		return text
	}
	var b strings.Builder
	at := 0
	for _, r := range refs {
		b.WriteString(text[at:r.From])
		b.WriteString(with(r))
		at = r.To
	}
	b.WriteString(text[at:])
	return b.String()
}

// PromptImage is one image the agent is given with the task, by its
// attachment id and the name lux passes it under.
type PromptImage struct{ ID, Name string }

// imageText is what the agent reads where a reference was: the image's
// place in the list it was given ("[Image 2: login.png]"), or that it is
// not there — another task's, removed, or never uploaded.
func imageText(images []PromptImage) func(ImageRef) string {
	return func(r ImageRef) string {
		for i, img := range images {
			if img.ID == r.ID {
				return "[Image " + strconv.Itoa(i+1) + ": " + img.Name + "]"
			}
		}
		return "[Image unavailable: " + r.name() + "]"
	}
}

// name is what a reader is told the image is: its alt, else its id.
func (r ImageRef) name() string {
	if r.Alt == "" {
		return r.ID
	}
	return r.Alt
}

// replaceEach is ReplaceImageRefs over each text, into a new slice.
func replaceEach(texts []string, with func(ImageRef) string) []string {
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = ReplaceImageRefs(t, with)
	}
	return out
}

// withImages is the input with each reference in the goal and criteria
// written as the agent reads it (imageText).
func (in PromptInput) withImages() PromptInput {
	text := imageText(in.Images)
	in.Goal = ReplaceImageRefs(in.Goal, text)
	in.AcceptanceCriteria = replaceEach(in.AcceptanceCriteria, text)
	return in
}

func nameText(r ImageRef) string { return "[Image: " + r.name() + "]" }

// ImagesAsText writes a task text's references for a reader given no
// images with it (a pull request, a note to a resumed agent): by name.
func ImagesAsText(text string) string {
	return ReplaceImageRefs(text, nameText)
}

// CriteriaImagesAsText is ImagesAsText for each criterion.
func CriteriaImagesAsText(criteria []string) []string {
	return replaceEach(criteria, nameText)
}
