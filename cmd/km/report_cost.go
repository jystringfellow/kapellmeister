package main

import (
	"encoding/json"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

type ReportCost struct {
	CostUSD      *json.Number `json:"cost_usd,omitempty"`
	CostComplete bool         `json:"cost_complete"`
	amount       *big.Rat
}

func (usage *reportUsage) readModels(data json.RawMessage) {
	var models map[string]*reportTokens
	if json.Unmarshal(data, &models) != nil {
		usage.modelsIncomplete = *usage.reportTokens != (reportTokens{})
		return
	}
	usage.models = map[string]reportTokens{}
	sum := reportTokens{}
	for model, tokens := range models {
		if tokens == nil || !validReportModelTokens(*tokens) {
			usage.modelsIncomplete = true
			continue
		}
		usage.models[model] = *tokens
		sum.add(*tokens)
	}
	if sum.InputTokens > usage.InputTokens || sum.OutputTokens > usage.OutputTokens || sum.CacheReadTokens > usage.CacheReadTokens || sum.CacheWriteTokens > usage.CacheWriteTokens {
		usage.models = nil
		usage.modelsIncomplete = true
		return
	}
	usage.modelsIncomplete = usage.modelsIncomplete || sum != *usage.reportTokens
}
func validReportModelTokens(tokens reportTokens) bool {
	for _, n := range []int64{tokens.InputTokens, tokens.OutputTokens, tokens.CacheReadTokens, tokens.CacheWriteTokens} {
		if n < 0 || n > 1<<36 {
			return false
		}
	}
	return true
}
func (tokens *reportTokens) add(other reportTokens) {
	tokens.InputTokens += other.InputTokens
	tokens.OutputTokens += other.OutputTokens
	tokens.CacheReadTokens += other.CacheReadTokens
	tokens.CacheWriteTokens += other.CacheWriteTokens
}
func mergeReportModels(total, models map[string]reportTokens) map[string]reportTokens {
	if total == nil && len(models) > 0 {
		total = map[string]reportTokens{}
	}
	for model, tokens := range models {
		sum := total[model]
		sum.add(tokens)
		total[model] = sum
	}
	return total
}
func decimalReportPrice(value float64) *big.Rat {
	price, _ := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, 64))
	return price
}
func priceReportModels(models map[string]reportTokens, complete bool, prices map[string]loop.ReportPrice) *ReportCost {
	cost := &ReportCost{amount: new(big.Rat), CostComplete: complete}
	anyPriced := false
	for model, tokens := range models {
		if tokens == (reportTokens{}) {
			continue
		}
		price, ok := prices[model]
		if !ok {
			cost.CostComplete = false
			continue
		}
		anyPriced = true
		input := new(big.Rat).Mul(new(big.Rat).SetInt64(tokens.InputTokens), decimalReportPrice(price.Input))
		output := new(big.Rat).Mul(new(big.Rat).SetInt64(tokens.OutputTokens), decimalReportPrice(price.Output))
		cost.amount.Add(cost.amount, input).Add(cost.amount, output)
		for _, cache := range []struct {
			tokens int64
			price  *float64
		}{{tokens.CacheReadTokens, price.CacheRead}, {tokens.CacheWriteTokens, price.CacheWrite}} {
			if cache.tokens == 0 {
				continue
			}
			if cache.price == nil {
				cost.CostComplete = false
				continue
			}
			amount := new(big.Rat).Mul(new(big.Rat).SetInt64(cache.tokens), decimalReportPrice(*cache.price))
			cost.amount.Add(cost.amount, amount)
		}
	}
	cost.amount.Quo(cost.amount, big.NewRat(1000000, 1))
	if anyPriced || cost.CostComplete {
		// With only factors of two and five, the denominator's bit length bounds the decimal places.
		text := cost.amount.FloatString(cost.amount.Denom().BitLen())
		value := json.Number(strings.TrimRight(strings.TrimRight(text, "0"), "."))
		cost.CostUSD = &value
	}
	return cost
}
func (usage reportUsage) cost(prices map[string]loop.ReportPrice) *ReportCost {
	return priceReportModels(usage.models, usage.Known && usage.UnknownSteps == 0 && !usage.modelsIncomplete, prices)
}
func (usage reportUsageTotal) cost(prices map[string]loop.ReportPrice) *ReportCost {
	return priceReportModels(usage.models, usage.KnownRuns > 0 && usage.UnknownRuns == 0 && usage.PartialRuns == 0 && !usage.modelsIncomplete, prices)
}
func (report *runReport) addCosts(prices map[string]loop.ReportPrice, source string) {
	report.priceSource = source
	unpriced := map[string]bool{}
	unpricedCache := map[string]map[string]bool{}
	note := func(models map[string]reportTokens) {
		for model, tokens := range models {
			if tokens == (reportTokens{}) {
				continue
			}
			price, ok := prices[model]
			if !ok {
				unpriced[model] = true
				continue
			}
			for key, missing := range map[string]bool{
				"cache_read":  tokens.CacheReadTokens > 0 && price.CacheRead == nil,
				"cache_write": tokens.CacheWriteTokens > 0 && price.CacheWrite == nil,
			} {
				if missing {
					if unpricedCache[model] == nil {
						unpricedCache[model] = map[string]bool{}
					}
					unpricedCache[model][key] = true
				}
			}
		}
	}
	for i := range report.Runs {
		run := &report.Runs[i]
		run.ReportCost = run.Usage.cost(prices)
		note(run.Usage.models)
	}
	report.Totals.Usage.ReportCost = report.Totals.Usage.cost(prices)
	for _, groups := range []map[string]reportUsageTotal{report.Totals.EnsembleUsage, report.Totals.RepositoryUsage} {
		for name, usage := range groups {
			usage.ReportCost = usage.cost(prices)
			groups[name] = usage
		}
	}
	if report.Parts != nil {
		for _, row := range report.Parts.Rows {
			row.ReportCost = row.Usage.cost(prices)
			note(row.Usage.models)
		}
	}
	for model := range unpriced {
		report.Totals.UnpricedModels = append(report.Totals.UnpricedModels, model)
	}
	sort.Strings(report.Totals.UnpricedModels)
	if len(unpricedCache) > 0 {
		report.Totals.UnpricedCacheModels = map[string][]string{}
		for model, keys := range unpricedCache {
			report.Totals.UnpricedCacheModels[model] = sortedKeys(keys)
		}
	}
}
func (cost *ReportCost) text() string {
	if cost == nil || cost.CostUSD == nil {
		return "unknown"
	}
	prefix := ""
	if !cost.CostComplete {
		prefix = "≥"
	}
	return prefix + reportMoney(cost.amount)
}
func reportMoney(amount *big.Rat) string {
	places := 2
	if amount.Sign() > 0 && amount.Cmp(big.NewRat(1, 1)) < 0 {
		scaled := new(big.Rat).Set(amount)
		places = 1
		for scaled.Cmp(big.NewRat(1, 1)) < 0 {
			scaled.Mul(scaled, big.NewRat(10, 1))
			places++
		}
	}
	text := amount.FloatString(places)
	if places > 2 && len(strings.TrimLeft(strings.ReplaceAll(text, ".", ""), "0")) > 2 {
		text = amount.FloatString(places - 1)
	}
	return "$" + text
}
