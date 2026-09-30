package lux_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// A dude Run's cost as prod lux answered it (lux docs/costs.md §8).
const prodCost = `{"runId":"run_oqgumhzpk67fpvxm","status":"final","final":true,"basis":"list",
 "totals":[{"currency":"USD","amount":"1.817906225","final":"1.817906225","estimate":"0"}],
 "byFamily":[
  {"family":"ai","displayName":"AI models","color":"violet","currency":"USD","amount":"1.810247","final":"1.810247","estimate":"0"},
  {"family":"compute","displayName":"Compute","currency":"USD","amount":"0.007659225","final":"0.007659225","estimate":"0"}],
 "lines":[
  {"source":"llm-proxy","family":"ai","item":"gpt-5.6-terra","amount":"1.810247","currency":"USD","final":true},
  {"source":"compute","family":"compute","item":"m8g.2xlarge:spot","amount":"0.007659225","currency":"USD","final":true}],
 "sources":[{"source":"compute","status":"final"},{"source":"llm-proxy","status":"final"}]}`

func costServer(t *testing.T, status int, body string) (string, *string) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &path
}

func TestCostReadsLuxsAnswerWithEveryDigit(t *testing.T) {
	url, path := costServer(t, 200, prodCost)
	c, err := lux.New(url, "k").Cost(context.Background(), "run_oqgumhzpk67fpvxm")
	if err != nil {
		t.Fatal(err)
	}
	if *path != "GET /v1/runs/run_oqgumhzpk67fpvxm/cost" {
		t.Errorf("asked %s", *path)
	}
	if c.Status != lux.CostFinal || !c.Final {
		t.Errorf("status %q final %v", c.Status, c.Final)
	}
	if ai, ok := c.FamilyUSD(lux.FamilyAI); !ok || ai != "1.810247" {
		t.Errorf("ai = %q %v", ai, ok)
	}
	// Nine decimal places: a float64 round trip would still hold these, but
	// the string is kept as lux wrote it.
	if compute, ok := c.FamilyUSD(lux.FamilyCompute); !ok || compute != "0.007659225" {
		t.Errorf("compute = %q %v", compute, ok)
	}
	if len(c.Totals) != 1 || c.Totals[0].Amount != "1.817906225" || len(c.Lines) != 2 || c.Lines[0].Source != "llm-proxy" {
		t.Errorf("totals/lines: %+v %+v", c.Totals, c.Lines)
	}
}

func TestCostIgnoresFamiliesInOtherCurrencies(t *testing.T) {
	url, _ := costServer(t, 200, `{"runId":"r","status":"incomplete","byFamily":[
		{"family":"ai","currency":"EUR","amount":"3.5","final":"0","estimate":"3.5"}]}`)
	c, err := lux.New(url, "k").Cost(context.Background(), "r")
	if err != nil {
		t.Fatal(err)
	}
	if ai, ok := c.FamilyUSD(lux.FamilyAI); ok {
		t.Errorf("a EUR amount read as USD: %q", ai)
	}
}

func TestCostRefusesAnAmountThatIsNotADecimalString(t *testing.T) {
	// Leading zeros are not a JSON number: the amount could not go into the
	// ledger event as one.
	for _, amount := range []string{`1.5`, `"1e3"`, `"NaN"`, `""`, `"007"`, `"01.5"`, `"1."`} {
		url, _ := costServer(t, 200, `{"runId":"r","status":"final","byFamily":[{"family":"ai","currency":"USD","amount":`+amount+`}]}`)
		if _, err := lux.New(url, "k").Cost(context.Background(), "r"); err == nil {
			t.Errorf("amount %s accepted", amount)
		}
	}
}

// A family without an amount, or with a null one, has reported none; nulls
// in the parts dude does not read leave the family's amount readable.
func TestCostWithoutAFamilyAmountIsNotReported(t *testing.T) {
	url, _ := costServer(t, 200, `{"runId":"r","status":"incomplete",
		"totals":[{"currency":"USD","amount":null,"final":null,"estimate":null}],
		"byFamily":[{"family":"ai","currency":"USD"},
		            {"family":"storage","currency":"USD","amount":null},
		            {"family":"compute","currency":"USD","amount":"0.5","final":null,"estimate":null}],
		"lines":[{"source":"compute","family":"compute","item":"x","amount":null,"currency":"USD"}]}`)
	c, err := lux.New(url, "k").Cost(context.Background(), "r")
	if err != nil {
		t.Fatal(err)
	}
	if ai, ok := c.FamilyUSD(lux.FamilyAI); ok {
		t.Errorf("a family with no amount read as %q", ai)
	}
	if s, ok := c.FamilyUSD("storage"); ok {
		t.Errorf("a family with a null amount read as %q", s)
	}
	if compute, ok := c.FamilyUSD(lux.FamilyCompute); !ok || compute != "0.5" {
		t.Errorf("compute = %q %v", compute, ok)
	}
}

func TestCostOfARunLuxDoesNotKnowIsNotFound(t *testing.T) {
	url, _ := costServer(t, 404, `{"error":{"code":"not_found","message":"no such run"}}`)
	_, err := lux.New(url, "k").Cost(context.Background(), "gone")
	if !lux.IsNotFound(err) {
		t.Errorf("got %v", err)
	}
}
