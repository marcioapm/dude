package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf16"
)

func init() {
	for name, cmd := range automationCommands {
		if _, dup := commands[name]; dup {
			panic("dude-cli: command defined twice: " + name)
		}
		commands[name] = cmd
	}
}

// automationReadOnly are the fields GET /v1/automations/:id answers that its
// PUT refuses (automationInputSchema is strict): automation update drops them,
// so `dude automation show ID` output, edited, is its input.
var automationReadOnly = []string{"id", "projectId", "projectName", "enabled", "owner", "ownerLeftAt",
	"webhookSecretHint", "incomingSecretHint", "incomingSecret", "publicOrigin", "nextRunAt", "lastRun", "canEdit", "canTransfer", "createdAt", "updatedAt"}

func autPath(id string, suffix string) string { return "/v1/automations/" + esc(id) + suffix }

// jsonArg is a JSON object from a file, or from stdin for -.
func (c *cli) jsonArg(what, arg string) (obj, error) {
	var raw []byte
	var err error
	if arg == "-" {
		raw, err = io.ReadAll(c.stdin)
	} else {
		raw, err = os.ReadFile(arg)
	}
	if err != nil {
		return nil, err
	}
	var o obj
	if err := json.Unmarshal(raw, &o); err != nil || o == nil {
		return nil, fmt.Errorf("%s is not a JSON object", what)
	}
	return o, nil
}

func testDeliveryID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "test-" + hex.EncodeToString(b)
}

// automationBrief is a list row: what it is, whether it is on, when it runs.
func automationBrief(a obj) obj {
	out := pick(a, "id", "name", "projectName", "kind", "enabled", "trigger", "nextRunAt", "lastRun")
	if owner, ok := a["owner"].(obj); ok {
		out["owner"] = owner["name"]
	}
	return out
}

func automationRequest(method, suffix string, body any) func(*flag.FlagSet) doer {
	return noFlags(func(c *cli, a []string) (any, error) {
		return c.request(method, autPath(a[0], suffix), body)
	})
}

var automationCommands = map[string]command{
	"automation list": {"PROJECT?", "the organisation's automations and every project's, filtered; with PROJECT, its own and the organisation's that also run there",
		func(fs *flag.FlagSet) doer {
			scope := fs.String("scope", "", "organization or project: only the organisation's, or only projects'")
			trigger := fs.String("trigger", "", "schedule or github")
			kind := fs.String("kind", "", "agent, command, server or webhook; agent:own or agent:ROLE for one kind of agent")
			enabled := fs.String("enabled", "", "true or false")
			mine := fs.Bool("mine", false, "only those you own")
			name := fs.String("name", "", "only those whose name contains this")
			return func(c *cli, a []string) (any, error) {
				if *scope != "" {
					if err := oneOf("--scope", *scope, "organization", "project"); err != nil {
						return nil, err
					}
				}
				if *trigger != "" {
					if err := oneOf("--trigger", *trigger, "schedule", "github", "incoming"); err != nil {
						return nil, err
					}
				}
				if *kind != "" {
					k, role, _ := strings.Cut(*kind, ":")
					if err := oneOf("--kind", k, "agent", "command", "server", "webhook"); err != nil {
						return nil, err
					}
					if role != "" && (k != "agent" || oneOf("", role, "own", "qa_browser", "reviewer", "investigator") != nil) {
						return nil, errors.New("--kind is agent:own, agent:qa_browser, agent:reviewer or agent:investigator for one kind of agent")
					}
				}
				if *enabled != "" {
					if err := oneOf("--enabled", *enabled, "true", "false"); err != nil {
						return nil, err
					}
				}
				if a[0] != "" {
					if *scope != "" || *trigger != "" || *kind != "" || *enabled != "" || *mine || *name != "" {
						return nil, errors.New("automation list: filters list the organisation's; a PROJECT's list takes none")
					}
					pid, err := c.projectID(a[0])
					if err != nil {
						return nil, err
					}
					o, err := c.getObj("/v1/projects/" + esc(pid) + "/automations")
					if err != nil {
						return nil, err
					}
					own, here := []obj{}, []obj{}
					for _, x := range list(o, "automations") {
						own = append(own, automationBrief(x))
					}
					for _, x := range list(o, "alsoRunningHere") {
						here = append(here, automationBrief(x))
					}
					return obj{"automations": own, "alsoRunningHere": here}, nil
				}
				owner := ""
				if *mine {
					owner = "mine"
				}
				o, err := c.getObj("/v1/automations" + query("scope", *scope, "trigger", *trigger, "kind", *kind,
					"enabled", *enabled, "owner", owner, "q", *name))
				if err != nil {
					return nil, err
				}
				out := []obj{}
				for _, x := range list(o, "automations") {
					out = append(out, automationBrief(x))
				}
				return out, nil
			}
		}},
	"automation show": {"ID", "one automation in full; --settings: only what automation update takes", func(fs *flag.FlagSet) doer {
		settings := fs.Bool("settings", false, "only its settings, the input automation update takes")
		return func(c *cli, a []string) (any, error) {
			o, err := c.getObj(autPath(a[0], ""))
			if err != nil || !*settings {
				return o, err
			}
			for _, k := range automationReadOnly {
				delete(o, k)
			}
			return o, nil
		}
	}},
	"automation create": {"PROJECT?", "a new automation, the organisation's or PROJECT's; it starts disabled, with defaults to change with automation update",
		func(fs *flag.FlagSet) doer {
			name := fs.String("name", "", "its name (required)")
			kind := fs.String("kind", "", "agent, command, server or webhook (required)")
			trigger := fs.String("trigger", "", "schedule, github or incoming (an incoming webhook: its verification secret is shown this once) (required)")
			preset := fs.String("preset", "", "with --kind agent: one of dude's roles, qa_browser, reviewer or investigator (default: its own agent)")
			tz := fs.String("tz", "", "a schedule's time zone, e.g. Europe/Lisbon (default: UTC)")
			return func(c *cli, a []string) (any, error) {
				if *name == "" || *kind == "" || *trigger == "" {
					return nil, errors.New("automation create: --name, --kind and --trigger are required")
				}
				if err := oneOf("--kind", *kind, "agent", "command", "server", "webhook"); err != nil {
					return nil, err
				}
				if err := oneOf("--trigger", *trigger, "schedule", "github", "incoming"); err != nil {
					return nil, err
				}
				body := obj{"name": *name, "kind": *kind, "trigger": *trigger}
				if *preset != "" {
					if *kind != "agent" {
						return nil, errors.New("automation create: --preset goes with --kind agent")
					}
					if err := oneOf("--preset", *preset, "qa_browser", "reviewer", "investigator"); err != nil {
						return nil, err
					}
					body["preset"] = *preset
				}
				setIf(body, "tz", *tz)
				if a[0] != "" {
					pid, err := c.projectID(a[0])
					if err != nil {
						return nil, err
					}
					body["projectId"] = pid
				}
				return c.post("/v1/automations", body)
			}
		}},
	"automation update": {"ID SETTINGS", "replace an automation's settings with SETTINGS, a JSON file or - (stdin): dude automation show ID output, edited",
		noFlags(func(c *cli, a []string) (any, error) {
			s, err := c.jsonArg("SETTINGS", a[1])
			if err != nil {
				return nil, err
			}
			if id, ok := s["id"].(string); ok && id != a[0] {
				return nil, fmt.Errorf("automation update: SETTINGS are %s's, not %s's", id, a[0])
			}
			for _, k := range automationReadOnly {
				delete(s, k)
			}
			return c.request("PUT", autPath(a[0], ""), s)
		})},
	"automation enable":  {"ID", "turn an automation on", automationRequest("PUT", "/enabled", obj{"enabled": true})},
	"automation disable": {"ID", "turn an automation off; a kept session stays", automationRequest("PUT", "/enabled", obj{"enabled": false})},
	"automation delete": {"ID", "delete an automation and its runs' history", noFlags(func(c *cli, a []string) (any, error) {
		if _, err := c.request("DELETE", autPath(a[0], ""), nil); err != nil {
			return nil, err
		}
		return obj{"deleted": a[0]}, nil
	})},
	"automation duplicate": {"ID", "copy an automation: off, \"NAME (copy)\", owned by you", automationRequest("POST", "/duplicate", nil)},
	"automation owner": {"ID PERSON", "hand an automation to another member (its owner or an admin); PERSON is an id from person list",
		noFlags(func(c *cli, a []string) (any, error) {
			return c.request("PUT", autPath(a[0], "/owner"), obj{"personId": a[1]})
		})},
	"automation secret": {"ID", "replace a webhook automation's signing secret, read from stdin (16 to 256 characters); it is never shown again",
		noFlags(func(c *cli, a []string) (any, error) {
			if c.prompt != nil {
				c.prompt("Signing secret for " + a[0] + ": ")
			}
			line, _ := bufio.NewReader(c.stdin).ReadString('\n')
			secret := strings.TrimSpace(line)
			if secret == "" {
				return nil, errors.New("automation secret: no secret on stdin")
			}
			// webhookSecretSchema's bounds are zod's string length: UTF-16 code units.
			if n := len(utf16.Encode([]rune(secret))); n < 16 || n > 256 {
				return nil, fmt.Errorf("automation secret: the secret is %d characters; it takes 16 to 256", n)
			}
			v, err := c.request("PUT", autPath(a[0], "/webhook-secret"), obj{"secret": secret})
			if err != nil {
				var he *httpError
				if errors.As(err, &he) {
					he.body = strings.ReplaceAll(he.body, secret, "…")
				}
				return nil, err
			}
			o, _ := v.(obj)
			return pick(o, "id", "name", "webhookSecretHint"), nil
		})},
	"automation run": {"ID", "run an automation now: a schedule as it is; on GitHub events, a past delivery or a test event; on an incoming webhook, a past delivery or a body",
		func(fs *flag.FlagSet) doer {
			delivery := fs.String("delivery", "", "a past delivery's id, from automation deliveries")
			body := fs.String("body", "", "a test event's body, or an incoming webhook's: a JSON file, or - for stdin")
			event := fs.String("event", "", "with --body: the GitHub event (default: the automation's)")
			action := fs.String("action", "", "with --body: its action, e.g. opened, written into the body when the body names none")
			deliveryID := fs.String("delivery-id", "", "with --body: its X-GitHub-Delivery (default: a made-up test-…)")
			return func(c *cli, a []string) (any, error) {
				if *body == "" {
					if *event != "" || *action != "" || *deliveryID != "" {
						return nil, errors.New("automation run: --event, --action and --delivery-id go with --body")
					}
					if *delivery != "" {
						return c.post(autPath(a[0], "/runs"), obj{"deliveryId": *delivery})
					}
					return c.post(autPath(a[0], "/runs"), obj{})
				}
				if *delivery != "" {
					return nil, errors.New("automation run: --delivery or --body, not both")
				}
				b, err := c.jsonArg("--body", *body)
				if err != nil {
					return nil, err
				}
				if *action != "" {
					// The run's filters read the action from the body, not testEvent.action.
					if own := b["action"]; own != nil && own != *action {
						return nil, fmt.Errorf("automation run: --action %s, but the body's action is %v", *action, own)
					}
					b["action"] = *action
				}
				ev := *event
				if ev == "" {
					aut, err := c.getObj(autPath(a[0], ""))
					if err != nil {
						return nil, err
					}
					t, _ := aut["trigger"].(obj)
					if str(t, "kind") == "incoming" {
						// An incoming webhook's test is the body itself.
						if *action != "" || *deliveryID != "" {
							return nil, errors.New("automation run: an incoming webhook takes --body alone")
						}
						return c.post(autPath(a[0], "/runs"), obj{"payload": b})
					}
					if ev = str(t, "event"); ev == "" {
						return nil, errors.New("automation run: a scheduled automation takes no test event")
					}
				}
				id := *deliveryID
				if id == "" {
					id = testDeliveryID()
				}
				te := obj{"event": ev, "action": nil, "deliveryId": id, "body": b}
				if *action != "" {
					te["action"] = *action
				}
				return c.post(autPath(a[0], "/runs"), obj{"testEvent": te})
			}
		}},
	"automation run-list": {"ID", "an automation's runs, newest first, and its kept session", func(fs *flag.FlagSet) doer {
		status := fs.String("status", "", "queued, running, succeeded, failed, skipped or cancelled")
		limit := fs.Int("limit", 0, "at most this many, 1 to 200 (default 50)")
		return func(c *cli, a []string) (any, error) {
			if *status != "" {
				if err := oneOf("--status", *status, "queued", "running", "succeeded", "failed", "skipped", "cancelled"); err != nil {
					return nil, err
				}
			}
			l := ""
			if *limit != 0 {
				if *limit < 1 || *limit > 200 {
					return nil, errors.New("--limit is 1 to 200")
				}
				l = strconv.Itoa(*limit)
			}
			return c.request("GET", autPath(a[0], "/runs")+query("status", *status, "limit", l), nil)
		}
	}},
	"automation run-show": {"ID RUN", "one run of an automation: what triggered it, its log's end, a Server's requests",
		noFlags(func(c *cli, a []string) (any, error) {
			return c.request("GET", autPath(a[0], "/runs/"+esc(a[1])), nil)
		})},
	"automation deliveries": {"ID", "the last 30 days' deliveries of a GitHub or incoming automation and what dude did with each, for automation run --delivery",
		automationRequest("GET", "/deliveries", nil)},
	"automation delivery": {"ID DELIVERY", "one delivery: its verdict, group key, headers (secrets as ••••) and body",
		noFlags(func(c *cli, a []string) (any, error) {
			return c.request("GET", autPath(a[0], "/deliveries/"+esc(a[1])), nil)
		})},
	"automation rotate": {"ID", "a new verification secret for an incoming webhook, shown this once; the old one stops working",
		automationRequest("POST", "/incoming-secret/rotate", nil)},
	"automation test": {"ID BODY", "whether an event body (a JSON file, or - for stdin) would run a GitHub automation, and which filter skips it if not",
		noFlags(func(c *cli, a []string) (any, error) {
			b, err := c.jsonArg("BODY", a[1])
			if err != nil {
				return nil, err
			}
			return c.post(autPath(a[0], "/test-event/evaluate"), obj{"body": b})
		})},
	"automation start-fresh": {"ID", "end an automation's kept session; its next run starts a new one",
		automationRequest("POST", "/start-fresh", nil)},
	"automation prompts": {"ID", "every saved version of an agent automation's prompt or brief, newest first",
		automationRequest("GET", "/prompt-history", nil)},
	"automation prompt-restore": {"ID VERSION", "save an earlier prompt version again as the newest; VERSION is its number or id, from automation prompts",
		noFlags(func(c *cli, a []string) (any, error) {
			version := a[1]
			if n, err := strconv.Atoi(version); err == nil {
				o, err := c.getObj(autPath(a[0], "/prompt-history"))
				if err != nil {
					return nil, err
				}
				version = ""
				for _, v := range list(o, "versions") {
					if num, _ := v["number"].(float64); int(num) == n {
						version = str(v, "id")
					}
				}
				if version == "" {
					return nil, fmt.Errorf("automation prompt-restore: %s has no prompt version %d", a[0], n)
				}
			}
			return c.post(autPath(a[0], "/prompt-history/restore"), obj{"versionId": version})
		})},
	"automation repos": {"", "every project's repositories, for an organisation automation's repoFilter",
		noFlags(func(c *cli, _ []string) (any, error) { return c.request("GET", "/v1/automations/repositories", nil) })},
	"automation next": {"CRON", "a schedule's next three times, as dude's scheduler computes them", func(fs *flag.FlagSet) doer {
		tz := fs.String("tz", "UTC", "its time zone, e.g. Europe/Lisbon")
		return func(c *cli, a []string) (any, error) {
			return c.post("/v1/automations/schedule/next", obj{"cron": a[0], "tz": *tz})
		}
	}},
	"automation subscriptions": {"", "the GitHub events the organisation is subscribed to, for a trigger's event",
		noFlags(func(c *cli, _ []string) (any, error) { return c.request("GET", "/v1/automations/subscriptions", nil) })},
}
