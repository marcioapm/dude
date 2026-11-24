package delivery

// PreviewReadyServer is one server line of PreviewReady: its name, and its
// URL when it has one.
type PreviewReadyServer struct{ Name, URL string }

// PreviewReady is what an agent says when a preview it started first
// serves (servers.announceReady): notices/preview-ready. A task preview
// (task true) asks for the task's change to be checked; a branch preview
// names branch.
func PreviewReady(servers []PreviewReadyServer, task bool, branch string) string {
	return render(previewReadyTmpl, struct {
		Servers []PreviewReadyServer
		Task    bool
		Branch  string
	}{servers, task, branch})
}

var previewReadyTmpl = mustTemplate("notices/preview-ready")
