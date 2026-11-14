package loop

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/BurntSushi/toml"
)

func LoadReportPrices(home string) (prices map[string]ReportPrice, source string, err error) {
	path := UserConfigPath(home)
	cfg, found, err := loadConfigFile(path, path)
	if err != nil {
		hasPrices := cfg.md.IsDefined("report", "prices") || len(cfg.Report.Prices) > 0
		var parseErr toml.ParseError
		if errors.As(err, &parseErr) {
			hasPrices = hasPrices || parseErr.LastKey == "report.prices" || strings.HasPrefix(parseErr.LastKey, "report.prices.")
		}
		if hasPrices {
			return nil, path, err
		}
		return nil, "", err
	}
	if !found {
		return nil, "", nil
	}
	prices, err = cfg.reportPrices()
	return prices, path, err
}

func (cfg loadedConfig) reportPrices() (map[string]ReportPrice, error) {
	if len(cfg.Report.Prices) == 0 {
		return nil, nil
	}
	prices := make(map[string]ReportPrice, len(cfg.Report.Prices))
	for _, model := range sortedKeys(cfg.Report.Prices) {
		key := fmt.Sprintf("%s: report.prices.%q", cfg.name, model)
		fields := cfg.Report.Prices[model]
		for _, field := range sortedKeys(fields) {
			if field != "input" && field != "output" && field != "cache_read" && field != "cache_write" {
				return nil, fmt.Errorf("%s.%s: unknown key", key, field)
			}
		}
		readPrice := func(field string, required bool) (*float64, error) {
			raw, found := fields[field]
			if !found && !required {
				return nil, nil
			}
			var value float64
			switch n := raw.(type) {
			case int64:
				value = float64(n)
			case float64:
				value = n
			default:
				return nil, fmt.Errorf("%s.%s: must be a number of 0 or more", key, field)
			}
			if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("%s.%s: must be a finite number of 0 or more", key, field)
			}
			return &value, nil
		}
		input, err := readPrice("input", true)
		if err != nil {
			return nil, err
		}
		output, err := readPrice("output", true)
		if err != nil {
			return nil, err
		}
		cacheRead, err := readPrice("cache_read", false)
		if err != nil {
			return nil, err
		}
		cacheWrite, err := readPrice("cache_write", false)
		if err != nil {
			return nil, err
		}
		price := ReportPrice{Input: *input, Output: *output, CacheRead: cacheRead, CacheWrite: cacheWrite}
		prices[model] = price
	}
	return prices, nil
}
