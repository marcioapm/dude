package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
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

// A branch past what the bounded scan reads is still found by its exact
// name, as GitHub spells it; a name GitHub does not have is not offered.
func TestABranchPastTheScanIsFoundByItsExactName(t *testing.T) {
	const total = 600
	listed, exact := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if name, ok := strings.CutPrefix(r.URL.Path, "/repos/acme/api/branches/"); ok {
			exact++
			if n, err := strconv.Atoi(strings.TrimPrefix(name, "Feature/")); err != nil || n >= total {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"message":"Branch not found"}`)
				return
			}
			fmt.Fprint(w, `{"name":"`+name+`","protected":false,"commit":{"sha":"abc"}}`)
			return
		}
		listed++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		var out []string
		for i := (page - 1) * 100; i < min(page*100, total); i++ {
			out = append(out, fmt.Sprintf(`{"name":"Feature/%03d"}`, i))
		}
		fmt.Fprint(w, "["+strings.Join(out, ",")+"]")
	}))
	defer srv.Close()
	gh := NewGitHub(Credential{Auth: "pat", Secret: "x", APIBaseURL: srv.URL})
	ctx := context.Background()

	names, _, err := gh.Branches(ctx, "acme/api", "Feature/599", 50)
	if err != nil || !slices.Equal(names, []string{"Feature/599"}) || listed != BranchListLimit/100 || exact != 1 {
		t.Fatalf("exact past the scan: %v listed=%d exact=%d err=%v", names, listed, exact, err)
	}
	if names, _, err := gh.Branches(ctx, "acme/api", "Feature/700", 50); err != nil || len(names) != 0 {
		t.Fatalf("a missing name was offered: %v %v", names, err)
	}
	names, more, err := gh.Branches(ctx, "acme/api", "Feature/", 50)
	if err != nil || len(names) != 50 || !more {
		t.Fatalf("suggestions unbounded: %d more=%v err=%v", len(names), more, err)
	}
	exact = 0
	if names, _, _ := gh.Branches(ctx, "acme/api", "Feature/004", 50); !slices.Equal(names, []string{"Feature/004"}) || exact != 0 {
		t.Fatalf("an exact name the scan found: %v, %d extra reads", names, exact)
	}
}
