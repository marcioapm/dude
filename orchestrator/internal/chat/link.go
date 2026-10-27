package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
)

// A brainstorm made with no project linked offers, under its reply, up to
// linkCandidatesMax projects to link (a choice each) and the session page.
const (
	linkCandidatesMax = 3
	linkValuePrefix   = "link:"
	linkHeading       = "No project is linked, so this session can't read any repository or CI. Link one:"
	anotherHeading    = "Link another project:"
	chooseLabel       = "Choose in dude"
)

// linkOffer is what a request's reply offered to link (chat_requests.link_offer):
// the reply as posted, its session, and the candidates a click names by index.
type linkOffer struct {
	Text       string          `json:"text"`
	LinkURL    string          `json:"linkUrl"`
	LinkLabel  string          `json:"linkLabel"`
	ChooseURL  string          `json:"chooseUrl"`
	Session    string          `json:"session"`
	Candidates []linkCandidate `json:"candidates"`
}

type linkCandidate struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

func (c linkCandidate) label() string { return clip(c.Key+" · "+c.Name, 75) }

// linkCandidates are the projects offered to a brainstorm with none linked,
// in order and once each: what the conversation is usually about, where its
// earlier requests went (most first), then the routing answer's
// maybe_projects. Only projects of w, which never holds a hidden one.
func linkCandidates(w World, maybe []Project) []linkCandidate {
	var out []linkCandidate
	add := func(p Project, ok bool) {
		if !ok || len(out) == linkCandidatesMax {
			return
		}
		for _, c := range out {
			if c.ID == p.ID {
				return
			}
		}
		out = append(out, linkCandidate{ID: p.ID, Key: p.Key, Name: p.Name})
	}
	for _, key := range w.Usual {
		add(w.project(key))
	}
	for _, h := range w.Learned {
		add(w.project(h.Key))
	}
	for _, p := range maybe {
		add(w.project(p.ID))
	}
	return out
}

// offerLinks puts the "Link a project" row on a new session's reply and
// keeps what it offered on the request.
func offerLinks(r *request, reply *Reply, session string, cands []linkCandidate) {
	r.offer = &linkOffer{Text: reply.Text, LinkURL: reply.LinkURL, LinkLabel: reply.LinkLabel, ChooseURL: reply.LinkURL, Session: session,
		Candidates: cands}
	reply.Row = r.offer.row(r.ID, nil)
}

// linkedLine is one project linked from a reply, and by whom.
type linkedLine struct {
	ProjectID, Key, Name, By string
}

// row is the offer's row with the candidates not linked yet, and a line
// for each that was. Its heading asks for a first project, or another.
func (o linkOffer) row(request string, linked []linkedLine) *Row {
	row := &Row{Heading: linkHeading, LinkURL: o.ChooseURL, LinkLabel: chooseLabel}
	if len(linked) > 0 {
		row.Heading = anotherHeading
	}
	for i, c := range o.Candidates {
		done := false
		for _, l := range linked {
			done = done || l.ProjectID == c.ID
		}
		if !done {
			row.Choices = append(row.Choices, Choice{Label: c.label(), Value: linkValuePrefix + request + ":" + strconv.Itoa(i)})
		}
	}
	if len(linked) > 0 && len(row.Choices) == 0 {
		row.Heading = ""
	}
	for _, l := range linked {
		row.Linked = append(row.Linked, fmt.Sprintf("%s linked %s", l.By, linkCandidate{Key: l.Key, Name: l.Name}.label()))
	}
	return row
}

// errLinkRefused rolls a click's transaction back once its refusal is known.
var errLinkRefused = errors.New("the link was refused")

// linkProject is a click on a "Link a project" choice: the person who
// asked, or an admin of the organisation, adds that candidate (with every
// repository of it) to the request's session, through the session's link
// rules; the reply then says who linked what. The value names the request
// and the candidate's index: what it links is read from the request, so a
// forged value links nothing that was not offered. Once per request and
// project: a second click, or the delivery tried again, links nothing twice
// and posts at most the one update.
func (s *Service) linkProject(ctx context.Context, d claimed, p Provider, in Interaction) error {
	reqID, idx, ok := strings.Cut(strings.TrimPrefix(in.Value, linkValuePrefix), ":")
	i, err := strconv.Atoi(idx)
	if !ok || err != nil {
		i = -1
	}
	var (
		inst       Installation
		installed  bool
		offer      linkOffer
		found      bool
		askedBy    string
		askerName  string
		message    string
		who        asker
		admin      bool
		providerID = p.Info().ID
	)
	if err := s.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
		if inst, installed, err = loadInstallation(ctx, tx, providerID); err != nil || !installed || inst.WorkspaceID != in.WorkspaceID {
			installed = false
			return err
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT link_offer, external_user_id, external_user_name, COALESCE(question_message_id, '') FROM chat_requests
			WHERE id = $1 AND provider = $2 AND external_workspace_id = $3 AND conversation_id = $4 AND link_offer IS NOT NULL`,
			reqID, providerID, in.WorkspaceID, in.ConversationID).Scan(&raw, &askedBy, &askerName, &message)
		if err != nil && !db.IsNotFound(err) {
			return err
		}
		found = err == nil && json.Unmarshal(raw, &offer) == nil && i >= 0 && i < len(offer.Candidates)
		if who, err = whoIs(ctx, tx, providerID, in.WorkspaceID, in.User.ID); err != nil || who.Person == "" {
			return err
		}
		return tx.QueryRow(ctx, `SELECT role = 'admin' FROM people WHERE id = $1`, who.Person).Scan(&admin)
	}); err != nil || !installed {
		return err
	}
	private := func(text string) error {
		s.noteFailure(ctx, d.Org, providerID, p.PostEphemeral(ctx, inst, in.ConversationID, in.ThreadID, in.User.ID, Reply{Text: text}))
		return nil
	}
	if !found {
		return private(delivery.MChatLinkNotOffered)
	}
	if who.Person == "" || (in.User.ID != askedBy && !admin) {
		return private(fmt.Sprintf(delivery.MChatLinkOnlyAskerOrAdmin, firstNonEmpty(askerName, "the person who asked")))
	}
	c := offer.Candidates[i]
	by := firstNonEmpty(who.Display, who.Name)
	var refusal string
	var post, linked bool
	err = s.DB.InOrg(ctx, d.Org, func(tx pgx.Tx) error {
		if err := delivery.LockSession(ctx, tx, offer.Session); err != nil {
			return err
		}
		var event string
		var posted bool
		err := tx.QueryRow(ctx, `SELECT event_id, posted_at IS NOT NULL FROM chat_request_links WHERE request_id = $1 AND project_id = $2`,
			reqID, c.ID).Scan(&event, &posted)
		if err == nil {
			// Linked from here already: only this click's own retry still
			// owes the reply its update.
			post = event == d.ID && !posted
			return nil
		}
		if !db.IsNotFound(err) {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM session_projects WHERE session_id = $1 AND project_id = $2)`,
			offer.Session, c.ID).Scan(&linked); err != nil {
			return err
		}
		if linked {
			// Linked in dude meanwhile: nobody linked it from here.
			refusal, linked = fmt.Sprintf(delivery.MChatLinkLinkedAlready, c.Key), false
			return errLinkRefused
		}
		links, err := delivery.AddSessionProject(ctx, tx, d.Org, offer.Session, c.ID)
		if refused := (delivery.LinkRefusal{}); errors.As(err, &refused) {
			refusal = linkRefusal(refused, c, p.Info().Name)
			return errLinkRefused
		}
		if err != nil {
			return err
		}
		if err := delivery.SessionEvent(ctx, tx, delivery.SessionRef(d.Org, offer.Session), delivery.EvSessionLinked, "person", who.Person,
			map[string]any{"projects": links, "linked": c.Key, "via": providerID, "viaName": p.Info().Name}); err != nil {
			return err
		}
		linked, post = true, true
		_, err = tx.Exec(ctx, `INSERT INTO chat_request_links (request_id, organization_id, project_id, project_key, project_name, linked_by,
				linked_by_name, event_id)
			SELECT $1, current_organization_id(), p.id, COALESCE(p.key_prefix, $3), p.name, $4, $5, $6 FROM projects p WHERE p.id = $2`,
			reqID, c.ID, c.Key, who.Person, by, d.ID)
		return err
	})
	if errors.Is(err, errLinkRefused) {
		return private(refusal)
	}
	if err != nil {
		return err
	}
	if linked && s.RunStarted != nil {
		// The session's agent takes the new repositories as a member's link
		// gives them: at its submit, or by the syncer's resume of a live one.
		s.RunStarted()
	}
	if !post {
		return nil
	}
	return s.postLinks(ctx, d.Org, p, inst, reqID, in.ConversationID, message, offer)
}

func linkRefusal(r delivery.LinkRefusal, c linkCandidate, provider string) string {
	switch r.Code {
	case "hidden_from_chat":
		return fmt.Sprintf(delivery.MChatLinkHidden, c.Key, provider)
	case "not_found":
		return fmt.Sprintf(delivery.MChatLinkGone, c.Key)
	}
	return fmt.Sprintf(delivery.MChatLinkRefused, c.Key, r.Msg)
}

// postLinks updates the reply to say every project linked from it, by
// whom, and to offer only the candidates left; then marks those links posted.
func (s *Service) postLinks(ctx context.Context, org string, p Provider, inst Installation, reqID, conversation, message string, o linkOffer) error {
	var linked []linkedLine
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT project_id, project_key, project_name, linked_by_name FROM chat_request_links
			WHERE request_id = $1 ORDER BY created_at, project_key`, reqID)
		if err != nil {
			return err
		}
		linked, err = pgx.CollectRows(rows, pgx.RowToStructByPos[linkedLine])
		return err
	}); err != nil {
		return err
	}
	if message == "" {
		// The click came before the reply's id was kept: the update waits for it.
		return Transient(errors.New("the reply offering the link has no message id yet"), 0)
	}
	err := p.UpdateReply(ctx, inst, conversation, message, Reply{Text: o.Text, LinkURL: o.LinkURL, LinkLabel: o.LinkLabel,
		Row: o.row(reqID, linked)})
	if err != nil {
		s.noteFailure(ctx, org, inst.Provider, err)
		if transient, _ := IsTransient(err); transient {
			return err
		}
		s.log().Warn("updating a chat reply's links failed", "provider", inst.Provider, "request", reqID, "error", err)
	}
	var ids []string
	for _, l := range linked {
		ids = append(ids, l.ProjectID)
	}
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE chat_request_links SET posted_at = now() WHERE request_id = $1 AND project_id = ANY($2) AND posted_at IS NULL`,
			reqID, db.NonNil(ids))
		return err
	})
}
