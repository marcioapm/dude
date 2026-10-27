package slack

import (
	"encoding/json"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
)

// message is a message as conversations.replies and conversations.history
// return it: a person's or a bot's, with the attachments and blocks a bot
// (a CI report, an alert) puts its words and links in.
type message struct {
	TS          string                 `json:"ts"`
	User        string                 `json:"user"`
	Text        string                 `json:"text"`
	BotID       string                 `json:"bot_id"`
	Username    string                 `json:"username"`
	BotProfile  *struct{ Name string } `json:"bot_profile"`
	Attachments []attachment           `json:"attachments"`
	Blocks      []json.RawMessage      `json:"blocks"`
}

type attachment struct {
	Pretext   string `json:"pretext"`
	Title     string `json:"title"`
	TitleLink string `json:"title_link"`
	Text      string `json:"text"`
	Fields    []struct {
		Title string `json:"title"`
		Value string `json:"value"`
	} `json:"fields"`
	Footer string `json:"footer"`
}

// threadMessage is m as dude reads it: its text, attachments and blocks
// joined (see messageText), and a bot's name.
func (m message) threadMessage() chat.ThreadMessage {
	out := chat.ThreadMessage{ID: m.TS, UserID: m.User, Text: messageText(m), Bot: m.BotID != ""}
	if out.Bot {
		out.BotName = m.Username
		if out.BotName == "" && m.BotProfile != nil {
			out.BotName = m.BotProfile.Name
		}
	}
	return out
}

// messageText is everything m shows as text, one part per line: its text,
// each attachment's pretext, title (with its link), text, fields and
// footer, and each block's text and link URLs. A part already said (Slack
// repeats text as a block) is left out. Unknown block types are skipped.
func messageText(m message) string {
	var parts textParts
	parts.add(m.Text)
	for _, a := range m.Attachments {
		parts.add(a.Pretext)
		title := a.Title
		if a.TitleLink != "" {
			title = strings.TrimSpace(title + " " + a.TitleLink)
		}
		parts.add(title)
		parts.add(a.Text)
		for _, f := range a.Fields {
			parts.add(strings.TrimSpace(strings.TrimSuffix(f.Title+": "+f.Value, ": ")))
		}
		parts.add(a.Footer)
	}
	for _, raw := range m.Blocks {
		parts.add(blockText(raw))
	}
	return strings.Join(parts.kept, "\n")
}

type textParts struct {
	kept []string
	seen string
}

// add keeps s unless it is empty or its words are already among those kept.
func (p *textParts) add(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	n := normalize(s)
	if n == "" || strings.Contains(p.seen, n) {
		return
	}
	p.kept = append(p.kept, s)
	p.seen += "\x00" + n
}

// normalize is s compared for repeats: Slack's link and mention brackets
// dropped, spaces collapsed, lower case.
func normalize(s string) string {
	s = strings.NewReplacer("<", " ", ">", " ", "|", " ").Replace(s)
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

type textObject struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// blockText is a Block Kit block's text: a section's text and fields, a
// header's text, a context's text elements, a rich_text's elements, and
// the URLs of buttons and images; "" for any other block.
func blockText(raw json.RawMessage) string {
	var b struct {
		Type      string            `json:"type"`
		Text      *textObject       `json:"text"`
		Fields    []textObject      `json:"fields"`
		Elements  []json.RawMessage `json:"elements"`
		Accessory *struct {
			URL string `json:"url"`
		} `json:"accessory"`
	}
	if json.Unmarshal(raw, &b) != nil {
		return ""
	}
	var out []string
	switch b.Type {
	case "section", "header":
		if b.Text != nil {
			out = append(out, b.Text.Text)
		}
		for _, f := range b.Fields {
			out = append(out, f.Text)
		}
		if b.Accessory != nil && b.Accessory.URL != "" {
			out = append(out, b.Accessory.URL)
		}
	case "context":
		for _, e := range b.Elements {
			var t textObject
			if json.Unmarshal(e, &t) == nil && (t.Type == "mrkdwn" || t.Type == "plain_text") {
				out = append(out, t.Text)
			}
		}
	case "actions":
		for _, e := range b.Elements {
			var btn struct {
				Text *textObject `json:"text"`
				URL  string      `json:"url"`
			}
			if json.Unmarshal(e, &btn) == nil && btn.URL != "" {
				label := ""
				if btn.Text != nil {
					label = btn.Text.Text
				}
				out = append(out, strings.TrimSpace(label+" "+btn.URL))
			}
		}
	case "rich_text":
		var b2 strings.Builder
		for _, e := range b.Elements {
			richText(&b2, e)
		}
		out = append(out, b2.String())
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// richText writes a rich_text element (a section, list, quote or
// preformatted run, or one of their leaves) as mrkdwn: a link as <url|text>,
// so it compares equal to the message's own text.
func richText(b *strings.Builder, raw json.RawMessage) {
	var e struct {
		Type     string            `json:"type"`
		Text     string            `json:"text"`
		URL      string            `json:"url"`
		Name     string            `json:"name"`
		UserID   string            `json:"user_id"`
		Channel  string            `json:"channel_id"`
		Elements []json.RawMessage `json:"elements"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return
	}
	switch e.Type {
	case "text":
		b.WriteString(e.Text)
	case "link":
		if e.Text != "" {
			b.WriteString("<" + e.URL + "|" + e.Text + ">")
		} else {
			b.WriteString("<" + e.URL + ">")
		}
	case "user":
		b.WriteString("<@" + e.UserID + ">")
	case "channel":
		b.WriteString("<#" + e.Channel + ">")
	case "emoji":
		b.WriteString(":" + e.Name + ":")
	case "rich_text_section", "rich_text_list", "rich_text_quote", "rich_text_preformatted":
		for _, c := range e.Elements {
			richText(b, c)
		}
		b.WriteString("\n")
	}
}
