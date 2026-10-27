package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shimCalls stands in for lux-shim and keeps each call: its arguments
// and a copy of the file it was given. It answers as lux does, with the
// file's sha256; the call numbered failOn exits 4 instead.
type shimCalls struct {
	t   *testing.T
	dir string
}

func recordingShim(t *testing.T, failOn int) *shimCalls {
	t.Helper()
	dir := t.TempDir()
	useShim(t, `#!/bin/sh
PATH=/usr/bin:/bin
n=$(( $(cat `+dir+`/n 2>/dev/null || echo 0) + 1 )); echo $n > `+dir+`/n
printf '%s\n' "$@" > `+dir+`/args.$n
for last; do :; done
cp "$last" `+dir+`/file.$n
if [ $n -eq `+fmt.Sprint(failOn)+` ]; then echo 'lux-shim publish: refused' >&2; exit 4; fi
sha=$(sha256sum "$last" | cut -d' ' -f1)
printf '{"id":"art_%016d","name":"n","size":1,"sha256":"%s"}\n' $n "$sha"
`)
	return &shimCalls{t, dir}
}

func (s *shimCalls) count() int {
	b, _ := os.ReadFile(filepath.Join(s.dir, "n"))
	var n int
	fmt.Sscan(string(b), &n)
	return n
}

func (s *shimCalls) args(n int) []string {
	b, _ := os.ReadFile(filepath.Join(s.dir, fmt.Sprintf("args.%d", n)))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (s *shimCalls) file(n int) []byte {
	b, _ := os.ReadFile(filepath.Join(s.dir, fmt.Sprintf("file.%d", n)))
	return b
}

// publishing runs `dude publish args...` and returns its answer and what
// it said on stderr about a thumbnail.
func publishing(t *testing.T, args ...string) (map[string]any, string, error) {
	t.Helper()
	var stderr strings.Builder
	old := thumbStderr
	thumbStderr = &stderr
	t.Cleanup(func() { thumbStderr = old })
	var out strings.Builder
	err := run(append([]string{"publish"}, args...), &out)
	var answer map[string]any
	if err == nil {
		if jerr := json.Unmarshal([]byte(out.String()), &answer); jerr != nil {
			t.Fatalf("answer %q: %v", out.String(), jerr)
		}
	}
	return answer, stderr.String(), err
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// noisy is a w×h picture of random pixels: as a PNG, far over 200 KiB
// once it is large enough.
func noisy(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = uint8(r.IntN(256))
	}
	return img
}

func filled(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	return img
}

func writePNG(t *testing.T, path string, img image.Image) []byte {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()
	b, _ := os.ReadFile(path)
	return b
}

func decodeJPEG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("the thumbnail is not a JPEG: %v", err)
	}
	return img
}

// A large image is published, then its thumbnail under .dude-thumbs/,
// named by the image's sha256: a JPEG at most 640 px on its longest side,
// named in the answer.
func TestALargeImageGetsAThumbnail(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	shim := recordingShim(t, 0)
	src := filepath.Join(t.TempDir(), "shot.png")
	orig := writePNG(t, src, noisy(1600, 900))
	answer, stderr, err := publishing(t, src, "--name", "shot.png", "--description", "the page")
	if err != nil || stderr != "" {
		t.Fatalf("publish: %v, stderr %q", err, stderr)
	}
	if shim.count() != 2 {
		t.Fatalf("lux-shim ran %d times; want the image, then its thumbnail", shim.count())
	}
	want := thumbsDir + sha(orig) + ".jpg"
	if got := strings.Join(shim.args(2)[:3], "|"); got != "publish|--name|"+want {
		t.Fatalf("the thumbnail was published with %q; want it named %s, no description", shim.args(2), want)
	}
	if answer["thumbnail"] != want || answer["id"] != "art_0000000000000001" || answer["sha256"] != sha(orig) {
		t.Fatalf("answered %v; want the image's answer with thumbnail %s", answer, want)
	}
	b := decodeJPEG(t, shim.file(2)).Bounds()
	if b.Dx() != 640 || b.Dy() != 360 {
		t.Fatalf("the thumbnail is %dx%d; want 640x360", b.Dx(), b.Dy())
	}
}

// An image already small is its own thumbnail; SVG and text get none.
// Neither says anything on stderr: nothing failed.
func TestSmallImagesAndOtherFilesGetNoThumbnail(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	dir := t.TempDir()
	small := filepath.Join(dir, "icon.png")
	writePNG(t, small, filled(600, 400, color.RGBA{10, 20, 30, 255}))
	svg := filepath.Join(dir, "logo.svg")
	os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="2000" height="2000"/>`), 0o644)
	md := filepath.Join(dir, "notes.md")
	os.WriteFile(md, []byte("# notes\n"), 0o644)
	for _, f := range []string{small, svg, md} {
		shim := recordingShim(t, 0)
		answer, stderr, err := publishing(t, f)
		if err != nil || stderr != "" || shim.count() != 1 || answer["thumbnail"] != nil {
			t.Fatalf("%s: %v, stderr %q, %d calls, answer %v; want one publish and no thumbnail", f, err, stderr, shim.count(), answer)
		}
	}
}

// A transparent image is flattened on white, not on black.
func TestATransparentImageIsFlattenedOnWhite(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	shim := recordingShim(t, 0)
	src := filepath.Join(t.TempDir(), "clear.png")
	img := noisy(1400, 1400)
	for y := range 200 {
		for x := range 200 {
			img.Set(x, y, color.RGBA{})
		}
	}
	writePNG(t, src, img)
	if _, _, err := publishing(t, src); err != nil || shim.count() != 2 {
		t.Fatalf("publish: %v, %d calls", err, shim.count())
	}
	r, g, b, _ := decodeJPEG(t, shim.file(2)).At(10, 10).RGBA()
	if r>>8 < 240 || g>>8 < 240 || b>>8 < 240 {
		t.Fatalf("a transparent corner is %d,%d,%d; want white", r>>8, g>>8, b>>8)
	}
}

// --poster makes a video's thumbnail from the picture given, scaled down.
func TestAPosterIsAVideosThumbnail(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	t.Setenv("PATH", t.TempDir()) // no ffmpeg: the poster alone
	shim := recordingShim(t, 0)
	dir := t.TempDir()
	video := filepath.Join(dir, "abc.webm")
	os.WriteFile(video, []byte("\x1a\x45\xdf\xa3 not really a video"), 0o644)
	poster := filepath.Join(dir, "04-saved.png")
	writePNG(t, poster, filled(1280, 720, color.RGBA{200, 30, 30, 255}))
	answer, stderr, err := publishing(t, video, "--name", "walkthrough.webm", "--poster", poster)
	if err != nil || stderr != "" || shim.count() != 2 {
		t.Fatalf("publish: %v, stderr %q, %d calls", err, stderr, shim.count())
	}
	if answer["thumbnail"] != thumbsDir+sha([]byte("\x1a\x45\xdf\xa3 not really a video"))+".jpg" {
		t.Fatalf("answered %v", answer)
	}
	thumb := decodeJPEG(t, shim.file(2))
	if b := thumb.Bounds(); b.Dx() != 640 || b.Dy() != 360 {
		t.Fatalf("the poster's thumbnail is %dx%d; want 640x360", b.Dx(), b.Dy())
	}
	if r, _, _, _ := thumb.At(320, 180).RGBA(); r>>8 < 180 {
		t.Fatalf("the thumbnail is not the poster")
	}
}

// --poster on anything but a video, or a poster that is no picture, is
// refused before anything is published.
func TestABadPosterPublishesNothing(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	dir := t.TempDir()
	poster := filepath.Join(dir, "p.png")
	writePNG(t, poster, filled(10, 10, color.Black))
	md := filepath.Join(dir, "notes.md")
	os.WriteFile(md, []byte("# notes\n"), 0o644)
	video := filepath.Join(dir, "v.mp4")
	os.WriteFile(video, []byte("video"), 0o644)
	notAPicture := filepath.Join(dir, "p.txt")
	os.WriteFile(notAPicture, []byte("hello"), 0o644)

	shim := recordingShim(t, 0)
	if _, _, err := publishing(t, md, "--poster", poster); err == nil || !strings.Contains(err.Error(), "usage") || shim.count() != 0 {
		t.Fatalf("--poster on a .md: %v, %d calls; want a usage error and nothing published", err, shim.count())
	}
	for p, want := range map[string]string{
		notAPicture:                       "--poster " + notAPicture + " is not a PNG, JPEG, GIF or WebP picture: ",
		filepath.Join(dir, "missing.png"): "--poster " + filepath.Join(dir, "missing.png") + ": open " + filepath.Join(dir, "missing.png") + ": no such file or directory",
	} {
		_, _, err := publishing(t, video, "--poster", p)
		if err == nil || !strings.HasPrefix(err.Error(), want) || shim.count() != 0 {
			t.Fatalf("--poster %s: %v, %d calls; want %q and nothing published", p, err, shim.count(), want)
		}
	}
}

// A video with no poster and no ffmpeg, or an ffmpeg that fails, is
// published as it always was: one call, its answer, one line on stderr.
func TestAVideoWithoutAThumbnailIsStillPublished(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	video := filepath.Join(t.TempDir(), "abc.webm")
	os.WriteFile(video, []byte("webm"), 0o644)
	failing := t.TempDir()
	os.WriteFile(filepath.Join(failing, "ffmpeg"), []byte("#!/bin/sh\necho 'Invalid data found when processing input' >&2\nexit 1\n"), 0o755)
	for _, path := range []string{t.TempDir(), failing} {
		t.Setenv("PATH", path)
		shim := recordingShim(t, 0)
		answer, stderr, err := publishing(t, video, "--name", "walkthrough.webm")
		if err != nil || shim.count() != 1 || answer["id"] != "art_0000000000000001" || answer["thumbnail"] != nil {
			t.Fatalf("PATH=%s: %v, %d calls, answer %v; want the video published alone", path, err, shim.count(), answer)
		}
		if !strings.HasPrefix(stderr, "dude publish: no thumbnail for walkthrough.webm: ") || strings.Count(stderr, "\n") != 1 {
			t.Fatalf("PATH=%s: stderr %q; want one line saying why", path, stderr)
		}
	}
}

// lux-shim refusing the thumbnail leaves the file's answer and success.
func TestAThumbnailRefusedLeavesTheFilesAnswer(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	shim := recordingShim(t, 2)
	src := filepath.Join(t.TempDir(), "shot.png")
	writePNG(t, src, noisy(1000, 1000))
	answer, stderr, err := publishing(t, src)
	if err != nil || shim.count() != 2 || answer["id"] != "art_0000000000000001" || answer["thumbnail"] != nil {
		t.Fatalf("%v, %d calls, answer %v; want the image's answer alone", err, shim.count(), answer)
	}
	if !strings.Contains(stderr, "dude publish: no thumbnail for shot.png: lux-shim publish exited 4") {
		t.Fatalf("stderr %q", stderr)
	}
}

// On an older lux, the thumbnail is copied into $LUX_ARTIFACTS beside the
// file, named by the file's sha256 computed here.
func TestAnOlderLuxGetsTheThumbnailCopiedToo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LUX_ARTIFACTS", dir)
	fakeShim(t, "", 9)
	src := filepath.Join(t.TempDir(), "shot.png")
	orig := writePNG(t, src, noisy(900, 700))
	answer, stderr, err := publishing(t, src)
	want := thumbsDir + sha(orig) + ".jpg"
	if err != nil || stderr != "" || answer["published"] != "shot.png" || answer["thumbnail"] != want {
		t.Fatalf("%v, stderr %q, answer %v", err, stderr, answer)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "shot.png")); string(b) != string(orig) {
		t.Fatal("the image was not copied")
	}
	b, err := os.ReadFile(filepath.Join(dir, want))
	if err != nil {
		t.Fatal(err)
	}
	if b := decodeJPEG(t, b).Bounds(); b.Dx() != 640 {
		t.Fatalf("the thumbnail is %dx%d", b.Dx(), b.Dy())
	}
}

func TestFreezesAreReadFromFFmpegsLog(t *testing.T) {
	log := `Input #0, matroska,webm, from 'v.webm':
[freezedetect @ 0x1] lavfi.freezedetect.freeze_start: 0
[freezedetect @ 0x1] lavfi.freezedetect.freeze_duration: 0.6
[freezedetect @ 0x1] lavfi.freezedetect.freeze_end: 0.6
[freezedetect @ 0x1] lavfi.freezedetect.freeze_start: 1.2
[freezedetect @ 0x1] lavfi.freezedetect.freeze_duration: 2
[freezedetect @ 0x1] lavfi.freezedetect.freeze_end: 3.2
frame=   40 fps=0.0 q=-0.0 size=N/A time=00:00:03.50 bitrate=N/A
[freezedetect @ 0x1] lavfi.freezedetect.freeze_start: 5.5
frame=   72 fps=0.0 q=-0.0 Lsize=N/A time=00:00:07.20 bitrate=N/A speed=60.9x`
	got := parseFreezes(log)
	want := []freeze{{0, 0.6}, {1.2, 3.2}, {5.5, -1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("parsed %v; want %v", got, want)
	}
	if d := decodedUntil(log); d != 7.2 {
		t.Fatalf("decoded until %v; want 7.2", d)
	}
}

func TestTheFrameIsTheEndOfTheLongestStillStretch(t *testing.T) {
	for _, c := range []struct {
		name     string
		freezes  []freeze
		duration float64
		want     []float64
	}{
		{"longest first, a tenth before its end", []freeze{{1.2, 3.2}, {4, 9}, {10, 11}}, 12, []float64{8.9, 3.1, 10.9, 6}},
		{"one starting before a second is skipped", []freeze{{0, 8}, {2, 3}}, 10, []float64{2.9, 5}},
		{"an open one ends with the video", []freeze{{1.5, 2.5}, {5, -1}}, 8, []float64{7.9, 2.4, 4}},
		{"none: the middle", nil, 7, []float64{3.5}},
		{"only early ones: the middle", []freeze{{0.2, 5}}, 6, []float64{3}},
	} {
		got := frameTimes(c.freezes, c.duration)
		if fmt.Sprintf("%.2f", got) != fmt.Sprintf("%.2f", c.want) {
			t.Errorf("%s: %v; want %v", c.name, got, c.want)
		}
	}
}

func TestABlankFrameIsToldFromAPage(t *testing.T) {
	// A white page with codec noise of a level or two.
	blank := filled(1280, 720, color.White)
	r := rand.New(rand.NewPCG(3, 4))
	for i := range blank.Pix {
		if i%4 != 3 {
			blank.Pix[i] -= uint8(r.IntN(3))
		}
	}
	if !nearBlank(blank) || !nearBlank(filled(640, 360, color.RGBA{30, 30, 40, 255})) {
		t.Fatal("a blank frame is not blank")
	}
	// The same page with one short line of text-like marks on it.
	page := filled(1280, 720, color.White)
	for y := 100; y < 114; y++ {
		for x := 100; x < 500; x++ {
			if x%7 < 3 {
				page.Set(x, y, color.Black)
			}
		}
	}
	if nearBlank(page) {
		t.Fatal("a page with a line of text is blank")
	}
}

// With a real ffmpeg, the thumbnail is the end of the longest still
// stretch that is not blank: past the page loading, past a longer blank
// one, and not the middle of the video (moving noise here).
func TestARealVideosThumbnailIsItsStillPage(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg on PATH")
	}
	t.Setenv("LUX_ARTIFACTS", "")
	video := filepath.Join(t.TempDir(), "walk.webm")
	// red 0.3 s, green 0.4 s, noise 0.5 s, white 3 s (blank), noise 0.5 s,
	// blue with a white box 2 s, noise 7 s: 13.7 s, its middle in noise.
	const s = "s=160x120:r=10"
	graph := "color=c=red:" + s + ":d=0.3[a];color=c=green:" + s + ":d=0.4[b];testsrc2=" + s + ":d=0.5[c];" +
		"color=c=white:" + s + ":d=3[d];mandelbrot=" + s + ",trim=duration=0.5,setpts=PTS-STARTPTS[e];" +
		"color=c=blue:" + s + ":d=2,drawbox=x=40:y=30:w=40:h=30:color=white:t=fill[f];testsrc2=" + s + ":d=7[g];" +
		"[a][b][c][d][e][f][g]concat=n=7:v=1:a=0,format=yuv420p[v]"
	if out, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y", "-filter_complex", graph,
		"-map", "[v]", "-c:v", "libvpx", "-b:v", "300k", video).CombinedOutput(); err != nil {
		t.Fatalf("making the video: %v\n%s", err, out)
	}
	shim := recordingShim(t, 0)
	answer, stderr, err := publishing(t, video)
	if err != nil || stderr != "" || shim.count() != 2 || answer["thumbnail"] == nil {
		t.Fatalf("%v, stderr %q, %d calls, answer %v", err, stderr, shim.count(), answer)
	}
	thumb := decodeJPEG(t, shim.file(2))
	r, g, b, _ := thumb.At(5, 5).RGBA()
	if r>>8 > 60 || g>>8 > 60 || b>>8 < 180 {
		t.Fatalf("the thumbnail's corner is %d,%d,%d; want the blue page", r>>8, g>>8, b>>8)
	}
	if r, g, b, _ := thumb.At(60, 45).RGBA(); r>>8 < 200 || g>>8 < 200 || b>>8 < 200 {
		t.Fatalf("the box is %d,%d,%d; want white", r>>8, g>>8, b>>8)
	}
}
