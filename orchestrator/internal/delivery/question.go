package delivery

import (
	"regexp"
	"strings"
)

// Question is what an agent asks a person when it cannot go on without a
// decision only a person can make.
type Question struct {
	Prompt  string
	Options []string
}

// A question is a fenced block whose info string is `question`, so it
// cannot be mistaken for code the agent is showing, and one reply holds at
// most one that counts: the last.
var questionBlock = regexp.MustCompile("(?s)```question[ \t]*\n(.*?)\n?```")

/*
ParseQuestion reads the question an agent ended its turn with, if any:

	```question
	Should `stats` with no PATH read standard input?
	- yes
	- no
	```

Lines starting with "- " are the choices offered; everything else is the
question. No choices means a free answer.
*/
func ParseQuestion(reply string) (Question, bool) {
	matches := questionBlock.FindAllStringSubmatch(reply, -1)
	if len(matches) == 0 {
		return Question{}, false
	}
	var q Question
	var prompt []string
	for _, line := range strings.Split(matches[len(matches)-1][1], "\n") {
		if option, ok := strings.CutPrefix(strings.TrimSpace(line), "- "); ok {
			if option = strings.TrimSpace(option); option != "" {
				q.Options = append(q.Options, option)
			}
			continue
		}
		prompt = append(prompt, line)
	}
	q.Prompt = strings.TrimSpace(strings.Join(prompt, "\n"))
	return q, q.Prompt != ""
}
