package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// A branch lookup reads pages of 100 and stops at BranchListLimit names
// read, saying the list was cut short; what matches is filtered as read.
func TestBranchesAreReadInBoundedPages(t *testing.T) {
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "[")
		for i := range 100 {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"name":"b-%d"}`, (n-1)*100+i)
		}
		fmt.Fprint(w, "]")
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL})
	names, more, err := gh.Branches(context.Background(), "acme/api", "b-4", 50)
	if err != nil || !more || pages != BranchListLimit/100 {
		t.Fatalf("names=%d more=%v pages=%d err=%v", len(names), more, pages, err)
	}
	if names[0] != "b-4" || names[1] != "b-40" {
		t.Fatalf("names %v", names[:3])
	}
	pages = 0
	names, more, err = gh.Branches(context.Background(), "acme/api", "", 3)
	if err != nil || !more || len(names) != 3 || pages != 1 {
		t.Fatalf("limited: %v more=%v pages=%d err=%v", names, more, pages, err)
	}
}
