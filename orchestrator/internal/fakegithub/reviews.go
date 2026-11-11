package fakegithub

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The review writes: POST /pulls/{n}/reviews and PUT
// /pulls/{n}/reviews/{id}/dismissals, answering as GitHub does, with the
// knobs on Server that make them fail (RefuseDismissal, ReviewTimeoutOnce,
// ReviewFail5xx).

// Messages GitHub answers the review writes with, in its 422's "errors"
// list or a refusal's message.
const (
	MessageNotInDiff      = "Pull request review thread line must be part of the diff"
	MessageStartNotInHunk = "Pull request review thread start line must be part of the same hunk as the line."
	MessageOwnApprove     = "Can not approve your own pull request"
	MessageOwnRequest     = "Can not request changes on your own pull request"
	MessageProtected      = "You are not authorized to dismiss this pull request review due to protected branch rules."
	MessageDismissed      = "Can not dismiss a review that has already been dismissed"
	MessageNotDismissable = "Can not dismiss a commented pull request review"
)

var reviewStates = map[string]string{"APPROVE": "APPROVED", "REQUEST_CHANGES": "CHANGES_REQUESTED", "COMMENT": "COMMENTED"}

// unprocessable is GitHub's 422 for a request it validated and refused:
// "Unprocessable Entity", with what was wrong in "errors".
func unprocessable(w http.ResponseWriter, detail string) {
	write(w, 422, map[string]any{"message": "Unprocessable Entity", "errors": []string{detail}})
}

// diffHunks is, per path, the hunk each line of base...commit's diff is
// in, by side ("RIGHT", "LEFT") and line number.
func (s *Server) diffHunks(base, commit string) map[string]map[string]map[int]int {
	out := map[string]map[string]map[int]int{}
	diff, err := s.git("diff", "--no-color", "--no-renames", "refs/heads/"+base+"..."+commit)
	if err != nil {
		return out
	}
	var sides map[string]map[int]int
	hunk, left, right := -1, 0, 0
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			sides, hunk = nil, -1
		case strings.HasPrefix(line, "+++ "):
			if path, ok := strings.CutPrefix(line, "+++ b/"); ok {
				sides = map[string]map[int]int{"RIGHT": {}, "LEFT": {}}
				out[path] = sides
			}
		case strings.HasPrefix(line, "--- "):
			if path, ok := strings.CutPrefix(line, "--- a/"); ok && sides == nil {
				sides = map[string]map[int]int{"RIGHT": {}, "LEFT": {}}
				out[path] = sides // a deleted file's diff names only its base
			}
		case strings.HasPrefix(line, "@@ ") && sides != nil:
			f := strings.Fields(line)
			left, _ = strconv.Atoi(strings.SplitN(strings.TrimPrefix(f[1], "-"), ",", 2)[0])
			right, _ = strconv.Atoi(strings.SplitN(strings.TrimPrefix(f[2], "+"), ",", 2)[0])
			hunk++
		case hunk >= 0 && sides != nil && line != "":
			switch line[0] {
			case ' ':
				sides["LEFT"][left], sides["RIGHT"][right] = hunk, hunk
				left++
				right++
			case '-':
				sides["LEFT"][left] = hunk
				left++
			case '+':
				sides["RIGHT"][right] = hunk
				right++
			}
		}
	}
	return out
}

// postReview is POST /pulls/{n}/reviews: a submitted review (no pending
// ones), on a commit the repository has, whose comments are each on a line
// of the diff, by the token's account, which GitHub does not let approve
// or request changes on its own pull request.
func (s *Server) postReview(w http.ResponseWriter, r *http.Request) {
	p := s.number(w, r)
	if p == nil {
		return
	}
	var in struct {
		CommitID string `json:"commit_id"`
		Event    string `json:"event"`
		Body     string `json:"body"`
		Comments []struct {
			Path      string `json:"path"`
			Line      int    `json:"line"`
			Side      string `json:"side"`
			StartLine int    `json:"start_line"`
			StartSide string `json:"start_side"`
			Body      string `json:"body"`
		} `json:"comments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, "Problems parsing JSON")
		return
	}
	s.mu.Lock()
	failing := s.ReviewFail5xx > 0
	if failing {
		s.ReviewFail5xx--
	}
	s.mu.Unlock()
	if failing {
		fail(w, http.StatusBadGateway, "Server Error")
		return
	}
	state, ok := reviewStates[in.Event]
	if !ok {
		unprocessable(w, "Event "+strconv.Quote(in.Event)+" is not one of APPROVE, REQUEST_CHANGES, COMMENT")
		return
	}
	if in.CommitID == "" {
		unprocessable(w, "commit_id is required")
		return
	}
	if _, err := s.git("cat-file", "-e", in.CommitID+"^{commit}"); err != nil {
		unprocessable(w, "No commit found for SHA: "+in.CommitID)
		return
	}
	hunks := map[string]map[string]map[int]int{}
	if len(in.Comments) > 0 {
		hunks = s.diffHunks(p.Base, in.CommitID)
	}
	for _, c := range in.Comments {
		side := c.Side
		if side == "" {
			side = "RIGHT"
		}
		h, ok := hunks[c.Path][side][c.Line]
		if !ok {
			unprocessable(w, MessageNotInDiff)
			return
		}
		if c.StartLine > 0 {
			startSide := c.StartSide
			if startSide == "" {
				startSide = side
			}
			if hs, ok := hunks[c.Path][startSide][c.StartLine]; !ok || hs != h {
				unprocessable(w, MessageStartNotInHunk)
				return
			}
		}
	}
	s.mu.Lock()
	poster := s.poster()
	if strings.EqualFold(poster, p.Author) && in.Event != "COMMENT" {
		s.mu.Unlock()
		if in.Event == "APPROVE" {
			unprocessable(w, MessageOwnApprove)
		} else {
			unprocessable(w, MessageOwnRequest)
		}
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rv := Review{ID: nextID.Add(1), Login: poster, State: state, Body: in.Body, SubmittedAt: now, CommitID: in.CommitID}
	p.Reviews = append(p.Reviews, rv)
	p.Requested = slices.DeleteFunc(p.Requested, func(l string) bool { return strings.EqualFold(l, poster) })
	for _, c := range in.Comments {
		p.LineComments = append(p.LineComments, Comment{ID: nextID.Add(1), Author: poster, Body: c.Body, Path: c.Path,
			CreatedAt: now, Line: c.Line, ReviewID: rv.ID})
	}
	out := s.reviewJSON(p, rv)
	drop := s.ReviewTimeoutOnce
	s.ReviewTimeoutOnce = false
	s.mu.Unlock()
	if drop {
		// Taken, and the answer never arrives: the connection is cut.
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
		return
	}
	write(w, 200, out)
}

// reviewJSON is a review as GitHub's review writes answer it. The caller
// holds s.mu.
func (s *Server) reviewJSON(p *Pull, rv Review) map[string]any {
	return map[string]any{"id": rv.ID, "node_id": "PRR_" + strconv.FormatInt(rv.ID, 10), "user": s.userJSON(rv.Login),
		"state": rv.State, "body": rv.Body, "submitted_at": rv.SubmittedAt, "commit_id": rv.CommitID,
		"html_url": "https://github.test/" + s.Slug + "/pull/" + strconv.Itoa(p.Number) + "#pullrequestreview-" + strconv.FormatInt(rv.ID, 10)}
}

// dismissReview is PUT /pulls/{n}/reviews/{id}/dismissals: an approval or
// a change request is dismissed, unless RefuseDismissal stands for the
// branch's protection.
func (s *Server) dismissReview(w http.ResponseWriter, r *http.Request) {
	p := s.number(w, r)
	if p == nil {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(p.Reviews, func(rv Review) bool { return rv.ID == id })
	switch {
	case i < 0:
		fail(w, 404, "Not Found")
	case s.RefuseDismissal:
		fail(w, 403, MessageProtected)
	case p.Reviews[i].State == "DISMISSED":
		unprocessable(w, MessageDismissed)
	case p.Reviews[i].State != "APPROVED" && p.Reviews[i].State != "CHANGES_REQUESTED":
		unprocessable(w, MessageNotDismissable)
	default:
		p.Reviews[i].State = "DISMISSED"
		write(w, 200, s.reviewJSON(p, p.Reviews[i]))
	}
}
