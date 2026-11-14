# Opt in to reporting parts and models from telemetry

`km report -parts` appends part/provider/model rows and adds a JSON `parts` object after the
same copy resolution and filters. Plain report keeps its output and first-line-only telemetry
reads; always reading full telemetry would add cost to report, status and profile guards.
A separate command would duplicate scanning and filtering. Rows prefer the configured chair model,
except when CAO explicitly reported that it ran another model. Verdict attribution matches subject,
round and chair name, with the last record winning; downstream facts can use unattributed verdicts.
A recorded review step missing its verdict prevents claiming unanimous approval.
Planner and implementer outcomes apply only when one model/provider played that part in the run;
escalations across models count as split runs. Counts and denominators describe facts, with no score
or ranking. Evidence: `TestPlainReportIgnoresTheStepRecordsAndPrintsWhatItAlwaysPrinted`,
`TestReportPartsShowsSeparateRowsForTwoModelsPlayingTheSamePart`,
`TestReportPartsAttributesVerdictsByRoundAndChairAndCountsDownstreamFacts`,
`TestReportPartsCountsRunOutcomesOnlyForPartsPlayedByOneModel` and
`TestReportPartsReadsARunRecordedByTheLoopItself`.
