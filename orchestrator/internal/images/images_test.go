package images

import (
	"strings"
	"testing"
)

func TestFinishContainerfileAddsTheLayerAndItsSetupLast(t *testing.T) {
	got := FinishContainerfile("r.example/dude/custom@sha256:aa", "r.example/dude/layer@sha256:bb")
	want := `FROM r.example/dude/custom@sha256:aa
COPY --from=r.example/dude/layer@sha256:bb /rootfs/ /
RUN ["/bin/sh", "/usr/local/share/dude/setup.sh"]
ENV OPENCODE_CONFIG=/usr/local/share/dude/opencode.json DISABLE_AUTOUPDATER=1 OPENCODE_DISABLE_AUTOUPDATE=1
USER agent
WORKDIR /home/agent
`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestTagsNameTheVersionAndTheLayer(t *testing.T) {
	layer := "r.example/dude/layer@sha256:0123456789abcdef0123"
	if got := FinalTag("r.example/dude/custom", "imv_1", layer); got != "r.example/dude/custom:imv_1-0123456789ab" {
		t.Errorf("FinalTag = %s", got)
	}
	if got := UserTag("r.example/dude/custom", "imv_1"); got != "r.example/dude/custom:imv_1-user" {
		t.Errorf("UserTag = %s", got)
	}
	for in, want := range map[string]string{
		"localhost:5000/dude/custom:imv_1-user": "localhost:5000/dude/custom",
		"localhost:5000/dude/custom@sha256:ab":  "localhost:5000/dude/custom",
		"debian":                                "debian",
	} {
		if got := Repository(in); got != want {
			t.Errorf("Repository(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestSubstituteReplacesLibraryImagesOnlyWhereAnImageIsNamed(t *testing.T) {
	refs := map[string]string{"acme-base": "r/c@sha256:1", "node-pnpm": "r/c@sha256:2"}
	in := "FROM image:acme-base AS a\n" +
		"FROM --platform=linux/arm64 image:node-pnpm\n" +
		"COPY --from=image:acme-base /x /y\n" +
		"RUN echo image:acme-base\n" +
		"FROM image:acme-base-two\n"
	want := "FROM r/c@sha256:1 AS a\n" +
		"FROM --platform=linux/arm64 r/c@sha256:2\n" +
		"COPY --from=r/c@sha256:1 /x /y\n" +
		"RUN echo image:acme-base\n" +
		"FROM image:acme-base-two\n"
	if got := Substitute(in, refs); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestTailKeepsTheEndAtALineStartAndSaysSo(t *testing.T) {
	tail := Tail{Max: 20}
	for range 10 {
		_, _ = tail.Write([]byte("line 012345\n"))
	}
	got := tail.String()
	if !strings.HasPrefix(got, "… (the start of the log is gone") {
		t.Fatalf("no notice: %q", got)
	}
	kept := strings.SplitN(got, "\n", 2)[1]
	if kept != "line 012345\n" {
		t.Errorf("kept %q", kept)
	}
	short := Tail{Max: 100}
	_, _ = short.Write([]byte("all of it\n"))
	if short.String() != "all of it\n" {
		t.Errorf("short = %q", short.String())
	}
}

func TestTailNeverCutsARune(t *testing.T) {
	tail := Tail{Max: 5}
	_, _ = tail.Write([]byte("ééééé"))
	kept := strings.SplitN(tail.String(), "\n", 2)[1]
	if strings.ToValidUTF8(kept, "?") != kept {
		t.Fatalf("cut a rune: %q", kept)
	}
}

func TestFailureSaysWhyInOneSentence(t *testing.T) {
	steps := "STEP 1/3: FROM debian\nSTEP 2/3: RUN apt-get update\nSTEP 3/3: RUN make\n"
	for _, c := range []struct {
		name, stage, log string
		timedOut         bool
		want             string
	}{
		{"oom", "building", steps + "error running container: exit status 137\nError: building at STEP \"RUN make\": exit status 137\n", false,
			"ran out of memory (1.5 GB) at step 3"},
		{"timeout", "building", steps, true, "took longer than the build's time limit at step 3"},
		{"a step", "building", steps + "Error: building at STEP \"RUN make\": exit status 2\n", false,
			"step 3 failed: building at STEP \"RUN make\": exit status 2"},
		{"push", "pushing", steps + "Error: writing blob: unauthorized\n", false, "pushing to the registry failed: writing blob: unauthorized"},
		{"no shell", "finishing", "STEP 3/6: RUN [\"/bin/sh\", \"/usr/local/share/dude/setup.sh\"]\nerror running container: exec: \"/bin/sh\": stat /bin/sh: no such file or directory\n", false,
			"the dude layer needs /bin/sh and glibc in the image"},
		{"no git", "finishing", "STEP 3/6: RUN …\ndude: the image needs git: agents commit with it\nError: exit status 1\n", false,
			"the image needs git: agents commit with it"},
		{"network", "building", "STEP 2/3: RUN curl x\ncurl: (6) Could not resolve host: no such host\nError: building at STEP: exit status 6\n", false,
			"a network request failed at step 2: building at STEP: exit status 6"},
	} {
		if got := Failure(c.stage, c.log, nil, "1536m", c.timedOut); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBuildArgsCarryTheLimits(t *testing.T) {
	c := CLI{Limits: Limits{Platform: "linux/arm64", CPUs: 1.5, Memory: "1536m"}, Authfile: "/run/auth.json", TLSVerify: true}
	got := strings.Join(c.BuildArgs("/tmp/ctx", "r/c:imv_1-user", map[string]string{"B": "2", "A": "1"}), " ")
	want := "build --platform linux/arm64 --cpu-period 100000 --cpu-quota 150000 --memory 1536m --memory-swap 1536m " +
		"--ulimit nproc=4096:4096 --pull=newer --layers=false --force-rm --authfile /run/auth.json " +
		"--build-arg A=1 --build-arg B=2 -t r/c:imv_1-user -f /tmp/ctx/Containerfile /tmp/ctx"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestHumanMemory(t *testing.T) {
	for in, want := range map[string]string{"1536m": "1.5 GB", "2g": "2 GB", "512m": "512 MB", "weird": "weird"} {
		if got := HumanMemory(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
