# km carries no prices

Prices change and differ by contract; a wrong built-in number is worse than none. Users supply
input/output prices per million tokens in their own `[report.prices]` table, matching recorded
model names exactly. Partial pricing is a labelled lower bound, and wholly unpriced usage stays
unknown. No price lookup or network dependency is added.

Cache reads and writes are priced only from the user's optional `cache_read` and `cache_write`
prices per model. Zero is a price; an absent cache price matters only when that model used those
tokens. Any cost including unpriced cache tokens is a lower bound, never exact: an exact-looking
figure far below the bill is worse than an incomplete one. Reports name the affected models and
missing keys. No default or inferred cache prices are added. Evidence:
`TestReportPricesAreReadFromTheUserConfigPerModelInDollarsPerMillionTokens`,
`TestReportShowsALowerBoundWhenOneModelIsUnpriced`,
`TestReportShowsUnknownCostWhenNothingCanBePriced`,
`TestReportOutputIsUnchangedWithoutPricesInPlainAndJSON`,
`TestReportCostWithCacheTokensAndNoCachePriceIsALowerBoundNotExact` and
`TestReportCachePriceOfZeroIsPricedAtNothingAndKeepsTheCostExact`.
