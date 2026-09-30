package lux

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// Cost statuses lux reports, least to most settled. Only final never
// changes again.
const (
	CostPending    = "pending"
	CostIncomplete = "incomplete"
	CostComplete   = "complete"
	CostFinal      = "final"
)

// Cost families dude shows apart.
const (
	FamilyAI      = "ai"
	FamilyCompute = "compute"
)

// Decimal is an amount as lux sends it: a decimal string, kept as text so
// no digit is lost to a float on the way to a numeric column. Held to the
// JSON number grammar, so it can be written into an event as a number.
// JSON null is "": not reported, like a missing amount.
type Decimal string

var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func (d *Decimal) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*d = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("amount %s: not a decimal string", b)
	}
	if !decimalPattern.MatchString(s) {
		return fmt.Errorf("amount %q: not a decimal", s)
	}
	*d = Decimal(s)
	return nil
}

// RunCost is GET /v1/runs/{id}/cost: what a Run cost so far, per currency,
// per family and per priced line. dude reads only a family's amount, so
// only that is a checked Decimal. The fields it does not read are strings
// and are not checked; a JSON number in one still fails decoding, which
// lux, sending every amount as a string, does not produce.
type RunCost struct {
	RunID    string       `json:"runId"`
	Status   string       `json:"status"`
	Final    bool         `json:"final"`
	Basis    string       `json:"basis"`
	Totals   []CostAmount `json:"totals"`
	ByFamily []FamilyCost `json:"byFamily"`
	Lines    []CostLine   `json:"lines"`
	Sources  []CostSource `json:"sources"`
}

type CostAmount struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
	Final    string `json:"final"`
	Estimate string `json:"estimate"`
}

type FamilyCost struct {
	Family      string  `json:"family"`
	DisplayName string  `json:"displayName"`
	Currency    string  `json:"currency"`
	Amount      Decimal `json:"amount"`
	Final       string  `json:"final"`
	Estimate    string  `json:"estimate"`
}

type CostLine struct {
	Source   string `json:"source"`
	Family   string `json:"family"`
	Item     string `json:"item"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Final    bool   `json:"final"`
}

type CostSource struct {
	Source string `json:"source"`
	Status string `json:"status"`
}

// FamilyUSD is the family's USD amount, and false when lux reports none
// (no such family, or one without an amount). Amounts in other currencies
// are not dude's to convert and are ignored.
func (c RunCost) FamilyUSD(family string) (Decimal, bool) {
	for _, f := range c.ByFamily {
		if f.Family == family && f.Currency == "USD" && f.Amount != "" {
			return f.Amount, true
		}
	}
	return "", false
}
