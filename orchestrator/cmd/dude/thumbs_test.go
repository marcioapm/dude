package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
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

// An image is its own thumbnail only when it is both at most 640 px and at
// most 200 KiB: either bound alone exceeded makes one.
func TestAnImageOverEitherSmallBoundGetsAThumbnail(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	dir := t.TempDir()
	heavy := filepath.Join(dir, "heavy.png")
	if b := writePNG(t, heavy, noisy(640, 400)); len(b) <= smallImageBytes {
		t.Fatalf("heavy.png is %d bytes; want over %d", len(b), smallImageBytes)
	}
	wide := filepath.Join(dir, "wide.png")
	if b := writePNG(t, wide, filled(1600, 900, color.RGBA{10, 20, 30, 255})); len(b) >= smallImageBytes {
		t.Fatalf("wide.png is %d bytes; want under %d", len(b), smallImageBytes)
	}
	for f, size := range map[string]image.Point{heavy: {640, 400}, wide: {640, 360}} {
		shim := recordingShim(t, 0)
		answer, stderr, err := publishing(t, f)
		if err != nil || stderr != "" || shim.count() != 2 || answer["thumbnail"] == nil {
			t.Fatalf("%s: %v, stderr %q, %d calls, answer %v; want the image and its thumbnail", f, err, stderr, shim.count(), answer)
		}
		if got := decodeJPEG(t, shim.file(2)).Bounds().Size(); got != size {
			t.Fatalf("%s: the thumbnail is %v; want %v", f, got, size)
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

	// By its first bytes alone (WebM's EBML), with no video extension in
	// its path or its name, it is still a video that takes a poster.
	bin := filepath.Join(dir, "abc.bin")
	os.WriteFile(bin, []byte("\x1a\x45\xdf\xa3 no extension says so"), 0o644)
	shim = recordingShim(t, 0)
	answer, stderr, err = publishing(t, bin, "--name", "walkthrough", "--poster", poster)
	if err != nil || stderr != "" || shim.count() != 2 || answer["thumbnail"] == nil {
		t.Fatalf("abc.bin: %v, stderr %q, %d calls, answer %v; want it and the poster's thumbnail", err, stderr, shim.count(), answer)
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

// A panic while making the thumbnail leaves the file's answer and success,
// and one line on stderr.
func TestAPanicMakingTheThumbnailLeavesTheFilesAnswer(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	old := thumbMaking
	thumbMaking = func() { panic("decoder exploded") }
	t.Cleanup(func() { thumbMaking = old })
	shim := recordingShim(t, 0)
	src := filepath.Join(t.TempDir(), "shot.png")
	writePNG(t, src, noisy(1000, 1000))
	answer, stderr, err := publishing(t, src)
	if err != nil || shim.count() != 1 || answer["id"] != "art_0000000000000001" || answer["thumbnail"] != nil {
		t.Fatalf("%v, %d calls, answer %v; want the image's answer alone", err, shim.count(), answer)
	}
	if stderr != "dude publish: no thumbnail for shot.png: panic: decoder exploded\n" {
		t.Fatalf("stderr %q", stderr)
	}
}

// A PNG whose header claims more than maxPixels is not decoded: the image
// is published alone and stderr says why. Only the header is real here,
// which is all image.DecodeConfig reads.
func TestAnImageTooLargeToDecodeGetsNoThumbnail(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	var buf bytes.Buffer
	if err := png.Encode(&buf, filled(1, 1, color.Black)); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	// Signature (8), IHDR length and type (8), then width and height.
	binary.BigEndian.PutUint32(b[16:], 8000)
	binary.BigEndian.PutUint32(b[20:], 8000)
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	src := filepath.Join(t.TempDir(), "huge.png")
	os.WriteFile(src, b, 0o644)
	if 8000*8000 <= maxPixels {
		t.Fatalf("8000x8000 is within maxPixels %d", maxPixels)
	}
	shim := recordingShim(t, 0)
	answer, stderr, err := publishing(t, src)
	if err != nil || shim.count() != 1 || answer["id"] != "art_0000000000000001" || answer["thumbnail"] != nil {
		t.Fatalf("%v, %d calls, answer %v; want the image published alone", err, shim.count(), answer)
	}
	if stderr != "dude publish: no thumbnail for huge.png: 8000x8000 is too large to decode\n" {
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

// realFFmpeg is the ffmpeg on PATH; the test is skipped without one, and
// says so loudly under CI, where the skip would otherwise go unnoticed.
func realFFmpeg(t *testing.T) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Skip("!!! CI HAS NO FFMPEG ON PATH: this real-ffmpeg thumbnail test DID NOT RUN; the fake-ffmpeg tests still cover videoFrame's choice !!!")
		}
		t.Skip("no ffmpeg on PATH")
	}
	return ffmpeg
}

// makeVideo encodes graph's [v] as a VP8 WebM at path, skipping the test
// when this ffmpeg cannot (a minimal build without lavfi or libvpx).
func makeVideo(t *testing.T, ffmpeg, graph, path string) {
	t.Helper()
	if out, err := exec.Command(ffmpeg, "-nostdin", "-v", "error", "-y", "-filter_complex", graph,
		"-map", "[v]", "-c:v", "libvpx", "-b:v", "300k", path).CombinedOutput(); err != nil {
		t.Skipf("this ffmpeg cannot make the test's VP8 WebM: %v\n%s", err, out)
	}
}

// bluePage asserts thumb is the blue frame with a white box over its
// three-eighths point.
func bluePage(t *testing.T, thumb image.Image) {
	t.Helper()
	b := thumb.Bounds()
	r, g, bl, _ := thumb.At(b.Min.X+5, b.Min.Y+5).RGBA()
	if r>>8 > 60 || g>>8 > 60 || bl>>8 < 180 {
		t.Fatalf("the thumbnail's corner is %d,%d,%d; want the blue page", r>>8, g>>8, bl>>8)
	}
	if r, g, bl, _ := thumb.At(b.Min.X+b.Dx()*3/8, b.Min.Y+b.Dy()*3/8).RGBA(); r>>8 < 200 || g>>8 < 200 || bl>>8 < 200 {
		t.Fatalf("the box is %d,%d,%d; want white", r>>8, g>>8, bl>>8)
	}
}

// With a real ffmpeg, the thumbnail is the end of the longest still
// stretch that is not blank: past the page loading, past a longer blank
// one, and not the middle of the video (moving noise here).
func TestARealVideosThumbnailIsItsStillPage(t *testing.T) {
	ffmpeg := realFFmpeg(t)
	t.Setenv("LUX_ARTIFACTS", "")
	video := filepath.Join(t.TempDir(), "walk.webm")
	// red 0.3 s, green 0.4 s, noise 0.5 s, white 3 s (blank), noise 0.5 s,
	// blue with a white box 2 s, noise 7 s: 13.7 s, its middle in noise.
	const s = "s=160x120:r=10"
	graph := "color=c=red:" + s + ":d=0.3[a];color=c=green:" + s + ":d=0.4[b];testsrc2=" + s + ":d=0.5[c];" +
		"color=c=white:" + s + ":d=3[d];mandelbrot=" + s + ",trim=duration=0.5,setpts=PTS-STARTPTS[e];" +
		"color=c=blue:" + s + ":d=2,drawbox=x=40:y=30:w=40:h=30:color=white:t=fill[f];testsrc2=" + s + ":d=7[g];" +
		"[a][b][c][d][e][f][g]concat=n=7:v=1:a=0,format=yuv420p[v]"
	makeVideo(t, ffmpeg, graph, video)
	shim := recordingShim(t, 0)
	answer, stderr, err := publishing(t, video)
	if err != nil || stderr != "" || shim.count() != 2 || answer["thumbnail"] == nil {
		t.Fatalf("%v, stderr %q, %d calls, answer %v", err, stderr, shim.count(), answer)
	}
	bluePage(t, decodeJPEG(t, shim.file(2)))
}

// With an ffmpeg and no ffprobe, the video's length is how far ffmpeg
// decoded, so a still stretch lasting to the end is closed there and chosen.
func TestWithoutFFprobeAStretchLastingToTheEndIsTheThumbnail(t *testing.T) {
	ffmpeg := realFFmpeg(t)
	t.Setenv("LUX_ARTIFACTS", "")
	video := filepath.Join(t.TempDir(), "walk.webm")
	// red 0.3 s, noise 4 s, then blue with a white box 3 s to the end: its
	// middle (3.65 s) is noise, and the first frame is blank.
	const s = "s=160x120:r=10"
	graph := "color=c=red:" + s + ":d=0.3[a];testsrc2=" + s + ":d=4[b];" +
		"color=c=blue:" + s + ":d=3,drawbox=x=40:y=30:w=80:h=30:color=white:t=fill[c];" +
		"[a][b][c]concat=n=3:v=1:a=0,format=yuv420p[v]"
	makeVideo(t, ffmpeg, graph, video)
	only := t.TempDir()
	if err := os.Symlink(ffmpeg, filepath.Join(only, "ffmpeg")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", only)
	if _, err := exec.LookPath("ffprobe"); err == nil {
		t.Fatal("ffprobe is still on PATH")
	}
	shim := recordingShim(t, 0)
	answer, stderr, err := publishing(t, video)
	if err != nil || stderr != "" || shim.count() != 2 || answer["thumbnail"] == nil {
		t.Fatalf("%v, stderr %q, %d calls, answer %v", err, stderr, shim.count(), answer)
	}
	bluePage(t, decodeJPEG(t, shim.file(2)))
}

// fakeFFmpeg puts on PATH, alone, an ffmpeg whose scan pass prints log and
// whose frame at -ss SECONDS is frames[SECONDS] (a PNG file), else blank.
// It answers the -ss values it was asked, in order.
func fakeFFmpeg(t *testing.T, log string, frames map[string]string, blank string) (asked func() []string) {
	t.Helper()
	dir := t.TempDir()
	logFile, askedFile := filepath.Join(dir, "log"), filepath.Join(dir, "asked")
	os.WriteFile(logFile, []byte(log), 0o644)
	cases := ""
	for at, png := range frames {
		cases += "  " + at + ") /bin/cat " + png + " ;;\n"
	}
	bin := filepath.Join(dir, "bin")
	os.Mkdir(bin, 0o755)
	script := `#!/bin/sh
case "$*" in *freezedetect*) /bin/cat ` + logFile + ` >&2; exit 0 ;; esac
while [ $# -gt 0 ]; do [ "$1" = -ss ] && at=$2; shift; done
echo "$at" >> ` + askedFile + `
case "$at" in
` + cases + `  *) /bin/cat ` + blank + ` ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return func() []string {
		b, _ := os.ReadFile(askedFile)
		return strings.Fields(string(b))
	}
}

// Of the still stretches ffmpeg reports, the longest is tried first; its
// frame being blank, the next one's page is the thumbnail. Every frame
// blank, there is none, said in one line. No real ffmpeg is needed.
func TestTheLongestStretchsBlankFrameGivesWayToTheNext(t *testing.T) {
	t.Setenv("LUX_ARTIFACTS", "")
	dir := t.TempDir()
	blank := filepath.Join(dir, "blank.png")
	writePNG(t, blank, filled(320, 240, color.White))
	page := filepath.Join(dir, "page.png")
	pageImg := filled(320, 240, color.RGBA{0, 0, 255, 255})
	for y := 60; y < 120; y++ {
		for x := 80; x < 240; x++ {
			pageImg.Set(x, y, color.White)
		}
	}
	writePNG(t, page, pageImg)
	video := filepath.Join(dir, "abc.webm")
	os.WriteFile(video, []byte("\x1a\x45\xdf\xa3 a video"), 0o644)
	// 2–8 s still (longest, blank at 7.9), 9–10 s still (the page at 9.9);
	// 12 s decoded, so the middle is 6 s.
	log := `[freezedetect @ 0x1] lavfi.freezedetect.freeze_start: 2
[freezedetect @ 0x1] lavfi.freezedetect.freeze_end: 8
[freezedetect @ 0x1] lavfi.freezedetect.freeze_start: 9
[freezedetect @ 0x1] lavfi.freezedetect.freeze_end: 10
frame=  120 fps=0.0 q=-0.0 Lsize=N/A time=00:00:12.00 bitrate=N/A speed=60x
`
	asked := fakeFFmpeg(t, log, map[string]string{"9.900": page}, blank)
	shim := recordingShim(t, 0)
	answer, stderr, err := publishing(t, video, "--name", "walk.webm")
	if err != nil || stderr != "" || shim.count() != 2 || answer["thumbnail"] == nil {
		t.Fatalf("%v, stderr %q, %d calls, answer %v", err, stderr, shim.count(), answer)
	}
	if got := strings.Join(asked(), " "); got != "7.900 9.900" {
		t.Fatalf("frames asked at %s; want 7.900 then 9.900", got)
	}
	bluePage(t, decodeJPEG(t, shim.file(2)))

	asked = fakeFFmpeg(t, log, nil, blank)
	shim = recordingShim(t, 0)
	answer, stderr, err = publishing(t, video, "--name", "walk.webm")
	if err != nil || shim.count() != 1 || answer["thumbnail"] != nil {
		t.Fatalf("all blank: %v, %d calls, answer %v; want the video alone", err, shim.count(), answer)
	}
	if got := strings.Join(asked(), " "); got != "7.900 9.900 6.000" {
		t.Fatalf("all blank: frames asked at %s; want 7.900 9.900 6.000", got)
	}
	if stderr != "dude publish: no thumbnail for walk.webm: no frame tried shows anything\n" {
		t.Fatalf("all blank: stderr %q", stderr)
	}
}
