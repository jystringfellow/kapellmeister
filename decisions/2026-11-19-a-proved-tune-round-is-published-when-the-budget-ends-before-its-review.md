# A proved tune round is published when the budget ends before its review

When `km tune verify` has a round whose verify is proved and the time budget is spent before the
reviewer has reviewed it, km publishes that proved round and ends with the outcome
`budget_exhausted`. This is intended, not an oversight.

The tune loop checks the budget after the proof and before it cues the reviewer, and again after
the review, before acting on the verdict. Either check ends the run through one path that copies
the best proved round's setup back to the checkout and appends
`best proved verify is round N's, copied back` to the reason. That is the path the loop already
takes when the reviewer asks for changes in the last round and no round remains. With no proved
round, the checkout is left untouched and the reason says so.

Why: the proof is km's own evidence, from clean runs, break runs and a run without network, and it
does not depend on the review. Throwing away a proved verify because a clock ran out would waste
the most expensive part of the run.

The outcome and its reason tell the caller the review did not happen: the outcome is
`budget_exhausted`, not `approved`, and the reason reads `time budget ... spent ... after round N`.

Rejected:

- Leaving the checkout untouched when the budget ends before the review. It discards a proved
  verify and treats the same proof differently from the no-round-remained case.
- Ending with `approved`. It would claim a review that did not happen.

Evidence: `TestTuneStopsAtAStepBoundaryAndNeverInterruptsAProofForItsBudget`, whose `prove` case
spends the budget during the proof and expects round 1 published, the outcome `budget_exhausted`
and no review.
