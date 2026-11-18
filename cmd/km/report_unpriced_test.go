package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func pricedAndUnpricedChairsFixture(t *testing.T, root string) {
	t.Helper()
	var steps []map[string]any
	for _, chair := range [][2]string{{"implement", "alpha"}, {"review", "beta"}} {
		step := partStep(chair[0], chair[1], "", 1)
		var usage map[string]any
		if err := json.Unmarshal([]byte(strings.ReplaceAll(alphaReportUsage, "alpha", chair[1])), &usage); err != nil {
			t.Fatal(err)
		}
		step["usage"] = usage
		steps = append(steps, step)
	}
	result := filepath.Join(partsFixture(t, root, "a", "approved", 1, steps...), "result.json")
	data, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, result, strings.TrimSuffix(string(data), "}")+`,"usage":`+mixedReportUsage+`}`)
}

func TestReportWarnsOnceOnStderrForEachModelWithUsageButNoPriceInPlainAndJSON(t *testing.T) {
	configureReportPrices(t, reportPriceConfig)
	root := recordRoot(t)
	pricedAndUnpricedChairsFixture(t, root)
	want := `warning: no price for model beta: its cost is unknown; add [report.prices."beta"] to ` + loop.UserConfigPath(loop.Home()) + "\n"
	for _, args := range [][]string{{root}, {"-json", root}, {"-parts", root}, {"-parts", "-json", root}} {
		_, diagnostics := captureReport(t, args...)
		if diagnostics != want {
			t.Fatalf("%v: diagnostics = %q, want %q", args, diagnostics, want)
		}
	}
}

func TestReportWarnsAboutNoModelWhenEveryModelWithUsageIsPriced(t *testing.T) {
	configureReportPrices(t, reportPriceConfig+"[report.prices.beta]\ninput=3\noutput=4\n")
	root := recordRoot(t)
	pricedAndUnpricedChairsFixture(t, root)
	if _, diagnostics := captureReport(t, "-parts", root); diagnostics != "" {
		t.Fatal(diagnostics)
	}
}

func TestReportEscapesModelNamesInTheNoPriceWarning(t *testing.T) {
	configureReportPrices(t, reportPriceConfig)
	root := recordRoot(t)
	reportUsageFixture(t, root, "a", "app", "quick", strings.ReplaceAll(alphaReportUsage, "alpha", `bad\n\u001b[31m`))
	_, diagnostics := captureReport(t, root)
	if strings.Contains(diagnostics, "\x1b") || strings.Count(diagnostics, "\n") != 1 || !strings.Contains(diagnostics, `warning: no price for model bad\n\x1b[31m:`) {
		t.Fatalf("%q", diagnostics)
	}
}

func TestReportPartsShowTokensWithCostUnknownOnlyForTheChairWhoseModelHasNoPrice(t *testing.T) {
	configureReportPrices(t, reportPriceConfig)
	root := recordRoot(t)
	pricedAndUnpricedChairsFixture(t, root)
	out, _ := captureReport(t, "-parts", root)
	table := strings.Split(strings.Split(out, "by part and model\n")[1], "\n\n")[0]
	rows := map[string][]string{}
	for _, line := range strings.Split(table, "\n")[1:] {
		rows[strings.Fields(line)[0]] = strings.Fields(line)
	}
	priced, unpriced := strings.Join(rows["implementer"], " "), strings.Join(rows["reviewer"], " ")
	if !strings.HasSuffix(priced, "1M/500k 1M/500k $2.00") || !strings.HasSuffix(unpriced, "1M/500k 1M/500k cost unknown") || strings.Count(out, "cost unknown") != 1 {
		t.Fatal(out)
	}
	found := partRows(t, root)
	requireReportCost(t, partRow(t, found, "implementer", "alpha"), float64(2), true)
	requireReportCost(t, partRow(t, found, "reviewer", "beta"), nil, false)
	if got := partRow(t, found, "reviewer", "beta")["unpriced_models"]; !reflect.DeepEqual(got, []any{"beta"}) {
		t.Fatal(got)
	}
	if _, present := partRow(t, found, "implementer", "alpha")["unpriced_models"]; present {
		t.Fatal(found)
	}
	totals := reportJSON(t, "-parts", root)["totals"].(map[string]any)
	requireReportCost(t, totals["usage"].(map[string]any), float64(2), false)
	if !strings.Contains(out, "cost: ≥$2.00") {
		t.Fatal(out)
	}
}

func TestReportPartsKeepALowerBoundForAChairWithBothPricedAndUnpricedModels(t *testing.T) {
	configureReportPrices(t, reportPriceConfig)
	root := recordRoot(t)
	step := partStep("implement", "chair-model", "", 1)
	var usage map[string]any
	if err := json.Unmarshal([]byte(mixedReportUsage), &usage); err != nil {
		t.Fatal(err)
	}
	step["usage"] = usage
	partsFixture(t, root, "a", "approved", 1, step)
	out, _ := captureReport(t, "-parts", root)
	if strings.Contains(out, "cost unknown") || !strings.Contains(strings.Split(out, "by part and model")[1], "≥$2.00") {
		t.Fatal(out)
	}
	if got := partRows(t, root)[0].(map[string]any)["unpriced_models"]; !reflect.DeepEqual(got, []any{"beta"}) {
		t.Fatal(got)
	}
}
