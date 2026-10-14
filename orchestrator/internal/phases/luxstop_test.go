package phases

import "testing"

func TestLuxStopWordsSayWhyInPlainWordsWithLuxsNumbers(t *testing.T) {
	for _, c := range []struct {
		stop luxStop
		want string
	}{
		{luxStop{Reason: "disk", UsedBytes: 10_790_000_000, LimitBytes: 10 << 30}, "lux stopped the Run: disk limit exceeded (10.8 GB of 10 GiB)"},
		{luxStop{Reason: "disk", UsedBytes: 21_500_000_000, LimitBytes: 20 << 30}, "lux stopped the Run: disk limit exceeded (21.5 GB of 20 GiB)"},
		{luxStop{Reason: "disk"}, "lux stopped the Run: disk limit exceeded"},
		{luxStop{Reason: "oom"}, "lux stopped the Run: out of memory (the container was killed)"},
		{luxStop{Reason: "timeout"}, "lux stopped the Run at its time limit: 4h of running"},
		{luxStop{Reason: "preempt"}, "lux stopped the Run to move it: its spot host was reclaimed"},
		{luxStop{Reason: "drain"}, "lux stopped the Run to move it: its host was drained"},
		{luxStop{Reason: "migrate"}, "lux stopped the Run to move it to another host"},
		{luxStop{Reason: "stop"}, "lux stopped the Run: an operator stopped it"},
		{luxStop{Reason: "terminate"}, "lux stopped the Run: it was terminated"},
		{luxStop{Reason: "quota"}, "lux stopped the Run: quota"},
	} {
		if got := luxStopWords(c.stop, "4h"); got != c.want {
			t.Errorf("%+v: %q, want %q", c.stop, got, c.want)
		}
	}
}

// lux's reason for ending the Run, read from its state's reason or, when
// that says nothing a person needs, from what it said as the placement
// began stopping; a stop of another placement is not this end's.
func TestTheStopOfARunsEndIsLuxsReasonForThatPlacement(t *testing.T) {
	disk := &luxStop{Reason: "disk", Epoch: 2, UsedBytes: 5, LimitBytes: 4}
	tr := &translator{}
	tr.LuxStop = disk
	for _, c := range []struct {
		state, reason string
		epoch         int
		want          string
	}{
		{"failed", "disk limit exceeded", 2, "disk"},
		{"failed", "disk limit exceeded; its snapshot was refused", 2, "disk"},
		{"failed", "exit code 137", 2, "disk"},
		{"failed", "killed: out of memory", 3, "oom"},
		{"failed", "timeout", 3, "timeout"},
		{"terminated", "terminated", 3, "terminate"},
		{"failed", "exit code 1", 3, ""},
		{"lost", "host lost", 3, ""},
	} {
		got := tr.stopOf(c.state, c.reason, c.epoch)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%s %q epoch %d: %+v, want none", c.state, c.reason, c.epoch, *got)
		case c.want != "" && (got == nil || got.Reason != c.want):
			t.Errorf("%s %q epoch %d: %+v, want %s", c.state, c.reason, c.epoch, got, c.want)
		}
	}
	if got := tr.stopOf("failed", "disk limit exceeded", 2); got.UsedBytes != 5 {
		t.Errorf("the numbers lux gave were dropped: %+v", got)
	}
}
