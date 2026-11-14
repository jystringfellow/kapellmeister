package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const reportPriceConfig = "[report.prices.alpha]\ninput=1\noutput=2\n"
const alphaReportUsage = `{"known":true,"input_tokens":1000000,"output_tokens":500000,"cache_read_tokens":0,"cache_write_tokens":0,"models":{"alpha":{"input_tokens":1000000,"output_tokens":500000}}}`
const mixedReportUsage = `{"known":true,"input_tokens":2000000,"output_tokens":500000,"cache_read_tokens":0,"cache_write_tokens":0,"models":{"alpha":{"input_tokens":1000000,"output_tokens":500000},"beta":{"input_tokens":1000000,"output_tokens":0}}}`

func configureReportPrices(t *testing.T, body string) {
	t.Helper()
	isolateReportConfig(t)
	writeFile(t, loop.UserConfigPath(loop.Home()), body)
}
func requireReportCost(t *testing.T, obj map[string]any, amount any, complete bool) {
	t.Helper()
	if obj["cost_complete"] != complete || obj["cost_usd"] != amount {
		t.Fatalf("cost = %v, want %v complete=%v", obj, amount, complete)
	}
}
func TestReportOutputIsUnchangedWithoutPricesInPlainAndJSON(t *testing.T) {
	configureReportPrices(t, "")
	root := recordRoot(t)
	reportUsageFixture(t, root, "one", "app", "quick", alphaReportUsage)
	partsFixture(t, root, "two", "approved", 1, partStep("implement", "alpha", "", 1))
	for _, args := range [][]string{{root}, {"-json", root}, {"-parts", root}, {"-parts", "-json", root}, {t.TempDir()}} {
		before, diag := captureReport(t, args...)
		writeFile(t, loop.UserConfigPath(loop.Home()), "[report]\n")
		after, diagAfter := captureReport(t, args...)
		if before != after || diag != diagAfter || strings.Contains(after, "cost") || strings.Contains(after, "COST") {
			t.Fatalf("output changed: %s / %s", before, after)
		}
		writeFile(t, loop.UserConfigPath(loop.Home()), "")
	}
}
func TestReportShowsExactCostForEveryRunAndTotalWhenEveryModelIsPriced(t *testing.T) {
	configureReportPrices(t, reportPriceConfig+"[report.prices.beta]\ninput=3\noutput=4\n")
	root := recordRoot(t)
	reportUsageFixture(t, root, "a", "app", "quick", mixedReportUsage)
	out, _ := captureReport(t, root)
	if !strings.Contains(out, "COST") || !strings.Contains(out, "cost: $5.00") || strings.Contains(out, "≥$") || strings.Contains(out, "unpriced models:") {
		t.Fatal(out)
	}
	doc := reportJSON(t, root)
	requireReportCost(t, doc["runs"].([]any)[0].(map[string]any), float64(5), true)
	totals := doc["totals"].(map[string]any)
	requireReportCost(t, totals["usage"].(map[string]any), float64(5), true)
	for key, name := range map[string]string{"ensemble_usage": "quick", "repository_usage": "app"} {
		requireReportCost(t, totals[key].(map[string]any)[name].(map[string]any), float64(5), true)
	}
}
func TestReportShowsALowerBoundWhenOneModelIsUnpriced(t *testing.T) {
	configureReportPrices(t, reportPriceConfig)
	root := recordRoot(t)
	reportUsageFixture(t, root, "a", "app", "quick", mixedReportUsage)
	out, _ := captureReport(t, root)
	if !strings.Contains(out, "cost: ≥$2.00") || !strings.HasSuffix(strings.TrimSpace(strings.Split(out, "\n")[1]), "≥$2.00") {
		t.Fatal(out)
	}
	doc := reportJSON(t, root)
	requireReportCost(t, doc["runs"].([]any)[0].(map[string]any), float64(2), false)
	totals := doc["totals"].(map[string]any)
	requireReportCost(t, totals["usage"].(map[string]any), float64(2), false)
	for key, name := range map[string]string{"ensemble_usage": "quick", "repository_usage": "app"} {
		requireReportCost(t, totals[key].(map[string]any)[name].(map[string]any), float64(2), false)
	}
	if strings.Count(out, "≥$2.00") != 4 {
		t.Fatal(out)
	}
}
func TestReportShowsALowerBoundWhenAStepsUsageIsUnknown(t *testing.T) {
	configureReportPrices(t, reportPriceConfig)
	root := recordRoot(t)
	reportUsageFixture(t, root, "a", "app", "quick", strings.Replace(alphaReportUsage, `"known":true`, `"known":true,"unknown_steps":1`, 1))
	out, _ := captureReport(t, root)
	if strings.Count(out, "≥$2.00") != 4 {
		t.Fatal(out)
	}
	requireReportCost(t, reportJSON(t, root)["runs"].([]any)[0].(map[string]any), float64(2), false)
}
func TestReportShowsUnknownCostWhenNothingCanBePriced(t *testing.T) {
	for _, usage := range []string{"", knownReportUsage, strings.ReplaceAll(alphaReportUsage, "alpha", "Alpha")} {
		configureReportPrices(t, reportPriceConfig)
		root := recordRoot(t)
		reportUsageFixture(t, root, "a", "app", "quick", usage)
		out, _ := captureReport(t, root)
		if !strings.Contains(out, "cost: unknown") || !strings.HasSuffix(strings.TrimSpace(strings.Split(out, "\n")[1]), "unknown") {
			t.Fatal(out)
		}
		requireReportCost(t, reportJSON(t, root)["runs"].([]any)[0].(map[string]any), nil, false)
	}
}
func TestReportNeverTreatsUnattributedTokensOrAnUnknownRunAsCompleteCost(t *testing.T) {
	for _, extraRun := range []bool{false, true} {
		configureReportPrices(t, reportPriceConfig)
		root := recordRoot(t)
		usage := alphaReportUsage
		if !extraRun {
			usage = strings.Replace(usage, `"input_tokens":1000000`, `"input_tokens":2000000`, 1)
		}
		reportUsageFixture(t, root, "a", "app", "quick", usage)
		if extraRun {
			reportUsageFixture(t, root, "b", "app", "quick", "")
		}
		requireReportCost(t, reportJSON(t, root)["totals"].(map[string]any)["usage"].(map[string]any), float64(2), false)
		out, _ := captureReport(t, root)
		if !strings.Contains(out, "cost: ≥$2.00") {
			t.Fatal(out)
		}
	}
}
func TestReportNamesTheModelsWithUsageButNoPriceOnOneLineAndMatchesNamesExactly(t *testing.T) {
	configureReportPrices(t, reportPriceConfig)
	root := recordRoot(t)
	for i, model := range []string{"alpha-extra", "Alpha", "unknown", "beta", "zero"} {
		usage := strings.ReplaceAll(alphaReportUsage, "alpha", model)
		if model == "zero" {
			usage = strings.ReplaceAll(strings.ReplaceAll(usage, "1000000", "0"), "500000", "0")
		}
		reportUsageFixture(t, root, fmt.Sprint(i), "app", "quick", usage)
	}
	out, _ := captureReport(t, root)
	want := "unpriced models: Alpha, alpha-extra, beta, unknown (add them under [report.prices] in " + loop.UserConfigPath(loop.Home()) + ")"
	if !strings.Contains(out, want) || strings.Count(out, "unpriced models:") != 1 {
		t.Fatal(out)
	}
	got := reportJSON(t, root)["totals"].(map[string]any)["unpriced_models"]
	if !reflect.DeepEqual(got, []any{"Alpha", "alpha-extra", "beta", "unknown"}) {
		t.Fatal(got)
	}
}
func TestReportJSONAddsCostFieldsBesideUsageAndKeepsEveryExistingField(t *testing.T) {
	configureReportPrices(t, "")
	root := recordRoot(t)
	reportUsageFixture(t, root, "a", "app", "quick", mixedReportUsage)
	before := reportJSON(t, root)
	writeFile(t, loop.UserConfigPath(loop.Home()), reportPriceConfig)
	after := reportJSON(t, root)
	requireReportCost(t, after["runs"].([]any)[0].(map[string]any), float64(2), false)
	var removeCosts func(any)
	removeCosts = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			delete(x, "cost_usd")
			delete(x, "cost_complete")
			delete(x, "unpriced_models")
			for _, child := range x {
				removeCosts(child)
			}
		case []any:
			for _, child := range x {
				removeCosts(child)
			}
		}
	}
	removeCosts(after)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("old fields changed: %v / %v", before, after)
	}
}
func TestReportCostOfSummedRunsEqualsPricingTheSummedTokensToTheCent(t *testing.T) {
	configureReportPrices(t, "[report.prices.alpha]\ninput=1.004\noutput=0\n")
	root, combined := recordRoot(t), recordRoot(t)
	usage := strings.ReplaceAll(alphaReportUsage, "500000", "0")
	reportUsageFixture(t, root, "a", "app", "quick", usage)
	reportUsageFixture(t, root, "b", "app", "quick", usage)
	reportUsageFixture(t, combined, "a", "app", "quick", strings.ReplaceAll(usage, "1000000", "2000000"))
	for _, r := range []string{root, combined} {
		out, _ := captureReport(t, r)
		if !strings.Contains(out, "cost: $2.01") {
			t.Fatal(out)
		}
		requireReportCost(t, reportJSON(t, r)["totals"].(map[string]any)["usage"].(map[string]any), 2.008, true)
	}
}
func TestReportFormatsMoneyWithTwoDecimalsFromOneDollarAndTwoSignificantDigitsBelow(t *testing.T) {
	for price, want := range map[string]string{"0": "$0.00", "0.1": "$0.10", "0.001": "$0.0010", "0.00999": "$0.010", "0.995": "$1.00", "0.996": "$1.00", "0.004249": "$0.0042", "0.0000000042": "$0.0000000042", "1": "$1.00", "1234.5": "$1234.50"} {
		t.Run(price, func(t *testing.T) {
			configureReportPrices(t, "[report.prices.alpha]\ninput="+price+"\noutput=0\n")
			root := recordRoot(t)
			reportUsageFixture(t, root, "a", "app", "quick", strings.ReplaceAll(alphaReportUsage, "500000", "0"))
			out, _ := captureReport(t, root)
			if !strings.Contains(out, "cost: "+want+"\n") {
				t.Fatal(out)
			}
		})
	}
}
func TestReportPartsRowsCarryCostFromEachStepsUsageModels(t *testing.T) {
	configureReportPrices(t, reportPriceConfig)
	root := recordRoot(t)
	step := partStep("implement", "chair-model", "", 1)
	var usage map[string]any
	json.Unmarshal([]byte(mixedReportUsage), &usage)
	step["usage"] = usage
	partsFixture(t, root, "a", "approved", 1, step)
	row := partRows(t, root)[0].(map[string]any)
	requireReportCost(t, row, float64(2), false)
	if row["model"] != "chair-model" {
		t.Fatal(row)
	}
	out, _ := captureReport(t, "-parts", root)
	if !strings.Contains(out, "unpriced models: beta") || !strings.Contains(strings.Split(out, "by part and model")[1], "≥$2.00") {
		t.Fatal(out)
	}
}
func TestReportEscapesModelNamesInTheUnpricedLine(t *testing.T) {
	configureReportPrices(t, reportPriceConfig)
	root := recordRoot(t)
	usage := strings.ReplaceAll(alphaReportUsage, "alpha", `bad\n\u001b[31m`)
	reportUsageFixture(t, root, "a", "app", "quick", usage)
	out, _ := captureReport(t, root)
	if strings.Contains(out, "\x1b") || !strings.Contains(out, `unpriced models: bad\n\x1b[31m`) {
		t.Fatal(out)
	}
}
func TestReportTableStaysWithinOneHundredColumnsWithCostShown(t *testing.T) {
	configureReportPrices(t, reportPriceConfig)
	root := recordRoot(t)
	usage := strings.ReplaceAll(strings.ReplaceAll(alphaReportUsage, "1000000", "68719476736"), "500000", "68719476736")
	reportUsageFixture(t, root, "a", strings.Repeat("repository", 10), "thorough", usage)
	out, _ := captureReport(t, root)
	if !strings.Contains(out, "COST") {
		t.Fatal(out)
	}
	for _, line := range strings.Split(strings.Split(out, "\n\n")[0], "\n") {
		if utf8.RuneCountInString(line) > 100 {
			t.Fatalf("wide row (%d): %s", utf8.RuneCountInString(line), line)
		}
	}
}
func TestConfigShowsReportPricesAndTheFileTheyCameFrom(t *testing.T) {
	configureReportPrices(t, builtInAgentConfig+reportPriceConfig)
	out, err := captureStdout(t, func() error { return configCommand([]string{"-worktree", t.TempDir(), "-cao", unreachableCAO}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"report prices (USD per million tokens; " + loop.UserConfigPath(loop.Home()) + ")", "alpha: input 1, output 2"} {
		if !strings.Contains(out, want) {
			t.Fatal(out)
		}
	}
	writeFile(t, filepath.Join(loop.Home(), loop.UserConfigFile), builtInAgentConfig)
	out, err = captureStdout(t, func() error { return configCommand([]string{"-worktree", t.TempDir(), "-cao", unreachableCAO}) })
	if err != nil || strings.Contains(out, "report prices") {
		t.Fatalf("%s: %v", out, err)
	}
}

func TestReportRejectsABadPriceEvenWhenDirectoriesAreExplicit(t *testing.T) {
	for _, body := range []string{"input=-1\noutput=2", "input=1\noutput='bad'"} {
		configureReportPrices(t, "[report.prices.alpha]\n"+body+"\n")
		var out, diag strings.Builder
		err := reportCommand([]string{t.TempDir()}, &out, &diag)
		if err == nil || !strings.Contains(err.Error(), loop.UserConfigPath(loop.Home())) || !strings.Contains(err.Error(), `report.prices."alpha"`) || out.Len() != 0 {
			t.Fatalf("error=%v output=%s", err, &out)
		}
	}
}

func TestReportPartsCostRemainsCompleteOnlyWhenEveryContributingStepsUsageIsPriced(t *testing.T) {
	for _, tc := range []struct {
		name, second, text string
		amount             any
		complete           bool
	}{
		{"priced", alphaReportUsage, "$4.00", float64(4), true},
		{"unknown", `{"known":false}`, "≥$2.00", float64(2), false},
		{"unattributed", knownReportUsage, "≥$2.00", float64(2), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configureReportPrices(t, reportPriceConfig)
			root := recordRoot(t)
			for i, usage := range []string{alphaReportUsage, tc.second} {
				step := partStep("implement", "chair-model", "", 1)
				var fields map[string]any
				if err := json.Unmarshal([]byte(usage), &fields); err != nil {
					t.Fatal(err)
				}
				step["usage"] = fields
				partsFixture(t, root, fmt.Sprint(i), "approved", 1, step)
			}
			requireReportCost(t, partRows(t, root)[0].(map[string]any), tc.amount, tc.complete)
			out, _ := captureReport(t, "-parts", root)
			if !strings.Contains(strings.Split(out, "by part and model")[1], tc.text) {
				t.Fatal(out)
			}
		})
	}
}

func TestReportPricesTheLiteralUnknownModelByExactName(t *testing.T) {
	configureReportPrices(t, strings.ReplaceAll(reportPriceConfig, "alpha", "unknown"))
	root := recordRoot(t)
	reportUsageFixture(t, root, "a", "app", "quick", strings.ReplaceAll(alphaReportUsage, "alpha", "unknown"))
	requireReportCost(t, reportJSON(t, root)["runs"].([]any)[0].(map[string]any), float64(2), true)
}

func TestReportRoundsADecimalPriceAtTheExactHalfCent(t *testing.T) {
	configureReportPrices(t, "[report.prices.alpha]\ninput=0.1\noutput=0\n")
	root := recordRoot(t)
	usage := strings.ReplaceAll(strings.ReplaceAll(alphaReportUsage, "1000000", "10050000"), "500000", "0")
	reportUsageFixture(t, root, "a", "app", "quick", usage)
	out, _ := captureReport(t, root)
	if !strings.Contains(out, "cost: $1.01\n") {
		t.Fatal(out)
	}
}

func TestReportCostFieldsCanBeDecodedAlongsideExistingRunAndTotalFields(t *testing.T) {
	configureReportPrices(t, reportPriceConfig)
	root := recordRoot(t)
	reportUsageFixture(t, root, "a", "app", "quick", "")
	out, _ := captureReport(t, "-json", root)
	var report runReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
}

func TestReportJSONKeepsFiniteCostsOutsideTheFloatRangeAsNumbers(t *testing.T) {
	for _, tc := range []struct{ price, tokens, want string }{{"1e308", "2000000", "2e308"}, {"1e-320", "1", "1e-326"}} {
		t.Run(tc.price, func(t *testing.T) {
			configureReportPrices(t, "[report.prices.alpha]\ninput="+tc.price+"\noutput=0\n")
			root := recordRoot(t)
			usage := strings.ReplaceAll(strings.ReplaceAll(alphaReportUsage, "1000000", tc.tokens), "500000", "0")
			reportUsageFixture(t, root, "a", "app", "quick", usage)
			var out, diag strings.Builder
			if err := reportCommand([]string{"-json", root}, &out, &diag); err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(strings.NewReader(out.String()))
			decoder.UseNumber()
			var doc map[string]any
			if err := decoder.Decode(&doc); err != nil {
				t.Fatal(err)
			}
			value := doc["runs"].([]any)[0].(map[string]any)["cost_usd"].(json.Number)
			got, ok := new(big.Rat).SetString(string(value))
			if !ok {
				t.Fatal(value)
			}
			want, _ := new(big.Rat).SetString(tc.want)
			if got.Cmp(want) != 0 {
				t.Fatalf("cost=%s want=%s", got, want)
			}
		})
	}
}

func TestReportRejectsPricesThatFailTOMLParsingEvenWhenDirectoriesAreExplicit(t *testing.T) {
	for name, body := range map[string]string{
		"float overflow":   "[report.prices.alpha]\ninput=1e400\noutput=2\n",
		"integer overflow": "[report.prices.alpha]\ninput=99999999999999999999\noutput=2\n",
		"duplicate model":  "[report.prices.alpha]\ninput=1\noutput=2\n[report.prices.alpha]\ninput=3\noutput=4\n",
		"array of tables":  "[[report.prices]]\ninput=1\noutput=2\n",
		"missing value":    "[report.prices.alpha]\ninput=\noutput=2\n",
	} {
		t.Run(name, func(t *testing.T) {
			configureReportPrices(t, body)
			root := recordRoot(t)
			reportUsageFixture(t, root, "a", "app", "quick", alphaReportUsage)
			for _, flags := range [][]string{nil, {"-json"}, {"-parts"}, {"-parts", "-json"}} {
				var out, diag strings.Builder
				err := reportCommand(append(flags, root), &out, &diag)
				if err == nil || !strings.Contains(err.Error(), loop.UserConfigPath(loop.Home())) || !strings.Contains(err.Error(), "report.prices") || out.Len() != 0 {
					t.Fatalf("flags=%v error=%v output=%s", flags, err, &out)
				}
			}
		})
	}
}

func TestReportAndConfigRejectANonTablePriceSetting(t *testing.T) {
	for _, body := range []string{"[report]\nprices='x'\n", "[report]\nprices=3\n", "[report]\nprices=true\n", "[report]\nprices=[]\n"} {
		t.Run(body, func(t *testing.T) {
			configureReportPrices(t, builtInAgentConfig+body)
			root := recordRoot(t)
			reportUsageFixture(t, root, "a", "app", "quick", alphaReportUsage)
			var out, diag strings.Builder
			reportErr := reportCommand([]string{root}, &out, &diag)
			configOut, configErr := captureStdout(t, func() error { return configCommand([]string{"-worktree", t.TempDir(), "-cao", unreachableCAO}) })
			for _, err := range []error{reportErr, configErr} {
				if err == nil || !strings.Contains(err.Error(), loop.UserConfigPath(loop.Home())) || !strings.Contains(err.Error(), "report.prices") {
					t.Errorf("error=%v report output=%s config output=%s", err, &out, configOut)
				}
			}
			if out.Len() != 0 || configOut != "" {
				t.Errorf("report output=%s config output=%s", &out, configOut)
			}
		})
	}
}
