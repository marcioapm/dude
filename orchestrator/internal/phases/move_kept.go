package phases

// moveKeptTold is the named seam for both branches of notices/move-kept.
func moveKeptTold(to, kept, why string, reset bool) string {
	return moveKept.Render(struct {
		To, Kept, Why string
		Reset         bool
	}{to, kept, why, reset})
}
