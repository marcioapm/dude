package forge

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFailureExcerptGoPositionsDoNotDisplaceExplicitError(t *testing.T) {
	var input strings.Builder
	for i := range 20 {
		fmt.Fprintf(&input, "    widget_test.go:%d: debug value %d\n", 10+i, i)
	}
	const diagnostic = "Error: Cannot find module 'left-pad'"
	const exit = "##[error]Process completed with exit code 1."
	input.WriteString(strings.Repeat("noise\n", 3000) + diagnostic + "\n" + strings.Repeat("progress\n", 100) + exit)
	for _, budget := range []int{4000, 16380} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			out := FailureExcerpt(input.String(), budget)
			for _, cut := range []int{budget, 976} {
				out = FailureExcerpt(out, cut)
				if !strings.Contains(out, diagnostic) || !strings.Contains(out, exit) || len(out) > cut || !utf8.ValidString(out) {
					t.Fatalf("cut %d lost explicit failure: %q", cut, out)
				}
			}
		})
	}
}

func TestFailureExcerptBareGoCompilerAtSmallBudget(t *testing.T) {
	const diagnostic = "main.go:7:2: undefined: Widget"
	const exit = "##[error]Process completed with exit code 1."
	input := strings.Repeat("setup noise\n", 3000) + diagnostic + "\n" + strings.Repeat("progress\n", 100) + exit
	out := FailureExcerpt(FailureExcerpt(input, 16380), 976)
	if !strings.Contains(out, diagnostic) || !strings.Contains(out, exit) || len(out) > 976 || !utf8.ValidString(out) {
		t.Fatalf("bare compiler diagnostic lost: %q", out)
	}
}

func TestFailureExcerptDiagnosticClassification(t *testing.T) {
	for _, line := range []string{
		"src/main.py:7:2: F401 unused import",
		"C:/src/main.py:7:2: F401 unused import",
		"main.go:7:2: undefined: Widget",
		"./main.go:7: undefined: Widget",
		"/src/main.go:7:2: cannot use x (variable of type string) as int value in assignment",
		"main.go:7:2: syntax error: unexpected name Widget",
		"[lane2] main.go:7:2: too many arguments in call to f",
		"src/main.ts(3,1): error TS2304: Cannot find name 'Widget'.",
	} {
		if failurePriority(line) < 0 {
			t.Errorf("diagnostic not anchored: %q", line)
		}
	}
	for _, line := range []string{"progress", strings.Repeat("x", 4095), "progress: 7:2: still working", "main.go: downloading", "main.go:7:2:", "https://host:443/path", strings.Repeat("x:", 2047)} {
		if got := failurePriority(line); got != -1 {
			t.Errorf("ordinary line anchored (%d): %q", got, line)
		}
	}
}

// Both scan benchmarks consume exactly 16 MiB, including the final partial line.
func BenchmarkFailureExcerptFullScan(b *testing.B) {
	for _, size := range []int{100, 4096} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			line := strings.Repeat("x", size-1) + "\n"
			b.SetBytes(logReadMax)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				r := &countingReader{r: io.LimitReader(repeatReader(line), logReadMax)}
				if _, err := collectFailure(r, 4000); err != nil || r.n != logReadMax {
					b.Fatalf("read=%d err=%v", r.n, err)
				}
			}
		})
	}
}

func BenchmarkFailureExcerptClassify4K(b *testing.B) {
	for name, line := range map[string]string{"plain": strings.Repeat("x", 4095), "colonNoise": strings.Repeat("x:", 2047)} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if failurePriority(line) != -1 {
					b.Fatal("ordinary line anchored")
				}
			}
		})
	}
}

func TestFailureExcerptTimeoutReview(t *testing.T) {
	for _, config := range []string{"timeout=30s", "config: read_timeout 30", "pytest --timeout=60", "timeout_method: thread"} {
		if got := failurePriority(config); got == 0 {
			t.Errorf("configuration ranked strongest: %q", config)
		}
	}
	for _, actual := range []string{"panic: test timed out after 10m0s", "E       Failed: Timeout >60.0s", "++++ Timeout ++++", "[lane2] Fatal error: deadlock"} {
		if got := failurePriority(actual); got != 0 {
			t.Errorf("timeout/panic not strongest: %q: %d", actual, got)
		}
	}
	var input strings.Builder
	input.WriteString(strings.Repeat("config timeout=30\n", 40))
	input.WriteString("panic: test timed out after 10m0s\nrunning tests:\n")
	for i := range 24 {
		fmt.Fprintf(&input, "    TestBlocked%d (9m)\n", i)
	}
	input.WriteString(strings.Repeat("stack\n", 100) + "panic: independent second failure\n" + strings.Repeat("cleanup\n", 100) + "exit status 2")
	for _, source := range []string{input.String(), strings.TrimPrefix(input.String(), strings.Repeat("config timeout=30\n", 40))} {
		out := FailureExcerpt(source, 900)
		for _, want := range []string{"panic: test timed out", "TestBlocked0", "TestBlocked3", "panic: independent second failure", "exit status 2"} {
			if !strings.Contains(out, want) {
				t.Errorf("lost %q: %s", want, out)
			}
		}
	}
}

func TestFailureExcerptAnnotationReview(t *testing.T) {
	r := CheckLogRead{Said: "src/widget.go:42: failure: undefined: Widget", Tail: strings.Repeat("x", 3990)}
	for _, said := range []string{r.Said, strings.Repeat("annotation noise\n", 1000) + r.Said} {
		r.Said = said
		out := r.Cut(4000)
		if !strings.Contains(out, "undefined: Widget") || len(out) > 4000 {
			t.Fatalf("actionable annotation lost: %q", out)
		}
	}
}

func TestFailureExcerptLatestWeakestReview(t *testing.T) {
	c := failureCollector{window: 1024}
	for range excerptSites {
		c.add("error: wrapper")
	}
	for i := range excerptSites {
		c.add(fmt.Sprintf("FAIL package%d", i))
	}
	c.add("--- FAIL: TestImportant")
	for _, s := range c.sites {
		if s.anchor.text == "FAIL package15" {
			t.Fatal("latest weakest site retained instead of earlier peer")
		}
	}
}

func TestFailureExcerptMeasurementReview(t *testing.T) {
	for _, capped := range []bool{false, true} {
		for _, lines := range [][]excerptLine{nil, {{1, ""}}, {{1, ""}, {2, ""}}, {{2, "two"}, {2, "two"}, {4, ""}, {7, "seven"}}, {{1, "one"}, {2, ""}, {3, ""}}} {
			copyLines := append([]excerptLine{}, lines...)
			if got, want := excerptLength(copyLines, 9, capped), len(renderLines(copyLines, 9, capped)); got != want {
				t.Fatalf("measurement %d != %d", got, want)
			}
		}
	}
	c := failureCollector{window: 64 << 10}
	for i := range 180 {
		c.add(fmt.Sprintf("error: %d %s", i, strings.Repeat("x", 3500)))
	}
	if allocs := testing.AllocsPerRun(10, func() { c.render(64 << 10) }); allocs > 20 {
		t.Fatalf("render allocates trial outputs: %.0f allocations", allocs)
	}
}

func BenchmarkFailureExcerptRender(b *testing.B) {
	c := failureCollector{window: 64 << 10}
	for i := range 180 {
		c.add(fmt.Sprintf("error: %d %s", i, strings.Repeat("x", 3500)))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		c.render(64 << 10)
	}
}

func BenchmarkFailureExcerptRejectedSites(b *testing.B) {
	line := "error: repeated diagnostic"
	b.ReportAllocs()
	for range b.N {
		c := failureCollector{window: 1024}
		for range 10000 {
			c.add(line)
		}
	}
}

func TestFailureExcerptRecutsAndPriorities(t *testing.T) {
	input := strings.Repeat("error: generic wrapper\n", 300) + "[lane1] --- FAIL: TestRoot (0.01s)\n" +
		strings.Repeat("noise\n", 200) + "src/main.py:7:2: F401 unused import\nFAILED tests/test_root.py::test_root - AssertionError\n" +
		strings.Repeat("noise\n", 200) + "panic: late root\nrunning tests:\n    TestHung (10m)\n" +
		strings.Repeat("stack\n", 200) + "FAIL example/root 600s\nexit status 1\nexit status 2\n" + strings.Repeat("cleanup\n", 100)
	out := FailureExcerpt(input, 650)
	for _, want := range []string{"--- FAIL: TestRoot", "F401", "FAILED tests/test_root", "panic: late root", "TestHung", "FAIL example/root", "exit status 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
	for _, budget := range []int{650, 400, 200, 80, 12, 2, 1} {
		out = FailureExcerpt(out, budget)
		if len(out) > budget || !utf8.ValidString(out) {
			t.Fatalf("budget %d: %q", budget, out)
		}
		for range 3 {
			if again := FailureExcerpt(out, budget); again != out {
				t.Fatalf("unstable recut %d: %q -> %q", budget, out, again)
			}
		}
		if strings.Contains(out, omissionMarker+"\n"+omissionMarker) {
			t.Fatalf("nested omission markers: %q", out)
		}
	}
}

func TestFailureExcerptRangesAndBounds(t *testing.T) {
	var c failureCollector
	c.window = 1024
	for i := range 10000 {
		c.add(fmt.Sprintf("error: site %d", i))
	}
	if len(c.sites) != excerptSites || len(c.before) > excerptBefore || len(c.tail) > 64 || c.tailBytes > 1024 {
		t.Fatalf("unbounded collector: sites=%d before=%d tail=%d bytes=%d", len(c.sites), len(c.before), len(c.tail), c.tailBytes)
	}
	lines := []excerptLine{{4, "four"}, {2, "two"}, {3, "three"}, {3, "three"}, {9, "nine"}}
	if out := renderLines(lines, 9, false); out != omissionMarker+"\ntwo\nthree\nfour\n"+omissionMarker+"\nnine" {
		t.Fatalf("ranges: %q", out)
	}
	reader := &countingReader{r: io.MultiReader(strings.NewReader("##[error]early\n"), io.LimitReader(repeatReader("noise\n"), 32<<20))}
	out, err := collectFailure(reader, 120)
	if err != nil || reader.n != logReadMax || !strings.Contains(out, "input capped") || len(out) > 120 {
		t.Fatalf("cap: read=%d out=%q err=%v", reader.n, out, err)
	}
	boom := readerFunc(func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF })
	if _, err := collectFailure(boom, 100); err != io.ErrUnexpectedEOF {
		t.Fatalf("read error: %v", err)
	}
}

func TestFailureExcerptRegression(t *testing.T) {
	input := "##[error]early wrapper error\n" + strings.Repeat("noise\n", 1000) +
		"[lane2] panic: test timed out after 10m0s\n[lane2] running tests:\n[lane2]     TestBlocked (9m59s)\n" +
		strings.Repeat("goroutine noise\n", 1000) + "FAIL example/service 600s\n##[error]Process completed with exit code 2.\n" + strings.Repeat("cleanup\n", 1000)
	out, err := collectFailure(strings.NewReader(input), 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"panic: test timed out", "running tests:", "TestBlocked", "FAIL example/service", "exit code 2."} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q: %s", want, out)
		}
	}
	if len(out) > 500 {
		t.Errorf("over budget: %d", len(out))
	}
}

func TestFailureExcerptLineCapRegression(t *testing.T) {
	out, err := collectFailure(strings.NewReader("panic: boom "+strings.Repeat("x", 10000)+"\nexit status 2\n"), 6000)
	if err != nil {
		t.Fatal(err)
	}
	short := FailureExcerpt("panic: root "+strings.Repeat("x", 10000)+"\nexit status 2\n"+strings.Repeat("cleanup\n", 100), 200)
	if !strings.Contains(short, "panic: root") || !strings.Contains(short, "line capped") || !strings.Contains(short, "exit status 2") {
		t.Fatalf("long anchor lost: %q", short)
	}
	if !strings.Contains(out, "line capped") || len(out) > 6000 {
		t.Fatalf("missing line cap marker or over budget: %d bytes", len(out))
	}
}

func TestFailureExcerptCutRegression(t *testing.T) {
	r := CheckLogRead{Said: strings.Repeat("annotation noise\n", 1000), Tail: "panic: important root cause\n" + strings.Repeat("noise\n", 1000) + "exit status 2"}
	out := r.Cut(160)
	if len(out) > 160 || !strings.Contains(out, "panic: important root cause") || !strings.Contains(out, "exit status 2") {
		t.Fatalf("anchors crowded out: %q", out)
	}
	for n := 1; n < 80; n++ {
		out := (CheckLogRead{Said: "你好世界", Tail: "panic: 世界\nexit status 2"}).Cut(n)
		if len(out) > n || !utf8.ValidString(out) {
			t.Fatalf("budget %d: %q (%d bytes)", n, out, len(out))
		}
	}
}
