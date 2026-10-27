package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// A published image or video gets a small JPEG beside it, under
// thumbsDir and named by the file's sha256, which the web shows on the
// file's card instead of downloading the original.
const (
	thumbsDir = ".dude-thumbs/"
	thumbEdge = 640
	// An image this small already is its own thumbnail.
	smallImageBytes = 200 << 10
	// Larger pictures are not decoded: their pixels alone would take
	// hundreds of megabytes.
	maxPixels    = 64 << 20
	videoTimeout = 60 * time.Second
)

// thumbStderr is where a thumbnail that could not be made is said.
var thumbStderr io.Writer = os.Stderr

// publishFile publishes FILE, then its thumbnail when it is an image or a
// video. The thumbnail fails open: FILE's answer and status stand, and one
// line on stderr says why there is none.
func publishFile(path, name, description, poster string) (json.RawMessage, error) {
	video := isVideo(path, name)
	var posterImg image.Image
	if poster != "" {
		if !video {
			return nil, errors.New("usage: --poster is for a video (mp4, m4v, webm, mov, ogv): dude publish VIDEO --poster IMAGE")
		}
		img, err := decodeFile(poster)
		var bad notAPicture
		if errors.As(err, &bad) {
			return nil, fmt.Errorf("--poster %s is not a PNG, JPEG, GIF or WebP picture: %w", poster, bad.err)
		}
		if err != nil {
			return nil, fmt.Errorf("--poster %s: %w", poster, err)
		}
		posterImg = img
	}
	raw, err := publish(path, name, description)
	if err != nil {
		return nil, err
	}
	label := cmp.Or(name, filepath.Base(path))
	say := func(why error) { fmt.Fprintf(thumbStderr, "dude publish: no thumbnail for %s: %v\n", label, why) }
	var img image.Image
	switch {
	case posterImg != nil:
		img = posterImg
	case video:
		img, err = videoFrame(path)
	default:
		img, err = imageToShrink(path)
	}
	if err != nil {
		say(err)
		return raw, nil
	}
	if img == nil {
		return raw, nil
	}
	sum, err := sha256Of(raw, path)
	if err != nil {
		say(err)
		return raw, nil
	}
	tmp, err := writeThumb(img)
	if err != nil {
		say(err)
		return raw, nil
	}
	defer os.Remove(tmp)
	thumb := thumbsDir + sum + ".jpg"
	if _, err := publish(tmp, thumb, ""); err != nil {
		say(err)
		return raw, nil
	}
	return withThumbnail(raw, thumb), nil
}

var videoExtensions = []string{".mp4", ".m4v", ".webm", ".mov", ".ogv"}

// isVideo: by the name's or the path's extension, else by the first bytes
// as the control plane sniffs them (an ISO media ftyp box, or WebM's EBML).
func isVideo(path, name string) bool {
	for _, n := range []string{name, path} {
		if slices.Contains(videoExtensions, strings.ToLower(filepath.Ext(n))) {
			return true
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 12)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	return len(head) >= 12 && string(head[4:8]) == "ftyp" ||
		len(head) >= 4 && bytes.Equal(head[:4], []byte{0x1a, 0x45, 0xdf, 0xa3})
}

// notAPicture is a file that opens and reads but does not decode.
type notAPicture struct{ err error }

func (e notAPicture) Error() string { return e.err.Error() }

// decodeFile returns the file system's own error (open, read, seek), a
// too-large error, or notAPicture.
func decodeFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	asDecodeError := func(err error) error {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			return err
		}
		return notAPicture{err}
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, asDecodeError(err)
	}
	if cfg.Width*cfg.Height > maxPixels {
		return nil, fmt.Errorf("%dx%d is too large to decode", cfg.Width, cfg.Height)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, asDecodeError(err)
	}
	return img, nil
}

// imageToShrink is FILE decoded when it is a PNG, JPEG, GIF or WebP worth
// a thumbnail; nil, and no error, for anything else (SVG, text) and for an
// image small enough to show as it is.
func imageToShrink(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, nil
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if max(cfg.Width, cfg.Height) <= thumbEdge && info.Size() <= smallImageBytes {
		return nil, nil
	}
	return decodeFile(path)
}

// writeThumb writes img, at most thumbEdge on its longest side and never
// enlarged, as a JPEG flattened on white, to a temporary file.
func writeThumb(img image.Image) (string, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return "", errors.New("the picture is empty")
	}
	if long := max(w, h); long > thumbEdge {
		w = max(1, int(math.Round(float64(w)*thumbEdge/float64(long))))
		h = max(1, int(math.Round(float64(h)*thumbEdge/float64(long))))
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	f, err := os.CreateTemp("", "dude-thumb-*.jpg")
	if err != nil {
		return "", err
	}
	err = jpeg.Encode(f, dst, &jpeg.Options{Quality: 80})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// sha256Of is FILE's sha256 as lux recorded it (its answer's), else
// computed here: an older lux's copy answers none.
func sha256Of(answer json.RawMessage, path string) (string, error) {
	var a struct {
		SHA256 string `json:"sha256"`
	}
	if json.Unmarshal(answer, &a) == nil && hexSHA256.MatchString(a.SHA256) {
		return a.SHA256, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// withThumbnail adds the thumbnail's name to FILE's answer, every other
// field as it was.
func withThumbnail(answer json.RawMessage, thumb string) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(answer, &fields) != nil || fields == nil {
		return answer
	}
	fields["thumbnail"], _ = json.Marshal(thumb)
	b, err := json.Marshal(fields)
	if err != nil {
		return answer
	}
	return b
}

// ---- a video's frame -------------------------------------------------------

// videoFrame is the frame of a video that best stands for it, by the
// ffmpeg on PATH: the end of the longest stretch the picture stood still
// (a page done drawing), skipping one that is near-blank.
func videoFrame(path string) (image.Image, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, errors.New("no --poster, and no ffmpeg on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), videoTimeout)
	defer cancel()
	// Low resolution: freezedetect only compares frames, and this pass
	// decodes all of them.
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-v", "info", "-threads", "2", "-i", path,
		"-an", "-vf", "scale=320:-2,freezedetect=n=0.003:d=0.5", "-f", "null", "-")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, ffmpegFailed(ctx, err, stderr.Bytes())
	}
	log := stderr.String()
	duration := probeDuration(ctx, path)
	if duration <= 0 {
		duration = decodedUntil(log)
	}
	for _, at := range frameTimes(parseFreezes(log), duration) {
		img, err := frameAt(ctx, ffmpeg, path, at)
		if err != nil {
			return nil, err
		}
		if img != nil && !nearBlank(img) {
			return img, nil
		}
	}
	return nil, errors.New("no frame tried shows anything")
}

func ffmpegFailed(ctx context.Context, err error, stderr []byte) error {
	if ctx.Err() != nil {
		return fmt.Errorf("ffmpeg took longer than %s", videoTimeout)
	}
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return fmt.Errorf("ffmpeg: %v: %s", err, last)
	}
	return fmt.Errorf("ffmpeg: %v", err)
}

// probeDuration is the container's duration in seconds by ffprobe, or 0.
func probeDuration(ctx context.Context, path string) float64 {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0
	}
	out, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		return 0
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || math.IsNaN(d) || math.IsInf(d, 0) {
		return 0
	}
	return d
}

var progressTime = regexp.MustCompile(`time=(\d+):(\d+):(\d+(?:\.\d+)?)`)

// decodedUntil is the last progress time= ffmpeg printed, in seconds: the
// duration when ffprobe gives none (a browser's WebM often has none).
func decodedUntil(log string) float64 {
	all := progressTime.FindAllStringSubmatch(log, -1)
	if len(all) == 0 {
		return 0
	}
	m := all[len(all)-1]
	h, _ := strconv.ParseFloat(m[1], 64)
	mi, _ := strconv.ParseFloat(m[2], 64)
	s, _ := strconv.ParseFloat(m[3], 64)
	return h*3600 + mi*60 + s
}

// freeze is one stretch freezedetect reported; end < 0 while it lasted to
// the end of the video.
type freeze struct{ start, end float64 }

var freezeLine = regexp.MustCompile(`lavfi\.freezedetect\.freeze_(start|end): (-?[0-9.]+)`)

func parseFreezes(log string) []freeze {
	var out []freeze
	for _, m := range freezeLine.FindAllStringSubmatch(log, -1) {
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		switch {
		case m[1] == "start":
			out = append(out, freeze{start: v, end: -1})
		case len(out) > 0 && out[len(out)-1].end < 0:
			out[len(out)-1].end = v
		}
	}
	return out
}

// Before this the page is usually still loading.
const settleSeconds = 1.0

// frameTimes are the moments to try, best first: the end of each stretch
// starting after settleSeconds, longest first, a tenth of a second before
// it ends; then the middle of the video.
func frameTimes(freezes []freeze, duration float64) []float64 {
	type stretch struct{ length, at float64 }
	var s []stretch
	for _, f := range freezes {
		end := f.end
		if end < 0 {
			end = duration
		}
		if f.start < settleSeconds || end <= f.start {
			continue
		}
		s = append(s, stretch{end - f.start, max(f.start, end-0.1)})
	}
	slices.SortStableFunc(s, func(a, b stretch) int { return cmp.Compare(b.length, a.length) })
	var at []float64
	for _, x := range s {
		at = append(at, x.at)
	}
	return append(at, duration/2)
}

// frameAt is the video's frame at seconds, at full size; nil when there is
// none there.
func frameAt(ctx context.Context, ffmpeg, path string, seconds float64) (image.Image, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-v", "error", "-threads", "2",
		"-ss", strconv.FormatFloat(seconds, 'f', 3, 64), "-i", path,
		"-an", "-frames:v", "1", "-f", "image2pipe", "-c:v", "png", "-")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, ffmpegFailed(ctx, err, stderr.Bytes())
	}
	// Past the last frame (a duration longer than the stream): none.
	if stdout.Len() == 0 {
		return nil, nil
	}
	return png.Decode(&stdout)
}

// blankLuma is the luma standard deviation (0–255) below which a frame is
// one colour with codec noise: a blank or loading page. One line of text
// on an otherwise empty page is well above it.
const blankLuma = 3.0

func nearBlank(img image.Image) bool {
	b := img.Bounds()
	// At most about 256×256 samples: enough to tell a blank frame.
	step := max(1, max(b.Dx(), b.Dy())/256)
	var n, sum, sumSq float64
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			r, g, bl, _ := img.At(x, y).RGBA()
			l := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bl)) / 257
			n++
			sum += l
			sumSq += l * l
		}
	}
	if n == 0 {
		return true
	}
	mean := sum / n
	return math.Sqrt(max(0, sumSq/n-mean*mean)) < blankLuma
}
