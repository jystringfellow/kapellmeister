# Bound tune and retain the last proved verify

The first outside Python repository had about 24,000 tests and fifteen CI workflows. After about
75 minutes it had no verify: round one's full suite hit the 10m clean-run timeout, round two took
about 9m per clean run including fresh dependency installation, and a comment review discarded
that proof before one timing-dependent test made the revision flaky. The shared guide now targets
about two minutes, never over five, with stable CI checks and a relevant unit subset, explicit
omissions and manual commands, and cached lockfile installation. Tune defaults to a 30m budget
checked between steps and a 5m limit per proof verification; direct prove and ordinary run retain
their behavior.

A conservative comparison retains proof for recognized wrapper scripts whose commands differ
only in comments, blank lines or diagnostic step labels. It records normalized command texts and
digests, and also requires identical config bytes and mode, script mode and ordered break-patch
contents and kinds. Unknown shell forms require proof again. The last proved setup survives later
failed or flaky revisions and exhausted rounds, with approval and notes identifying what was not
adopted. Budget exhaustion takes precedence over that approval rule: it remains a distinct
`budget_exhausted` outcome with a nonzero exit, copying back the best proof when present. Interrupts
and chair errors never publish. Evidence: tune command-comparison, boundary-budget and fallback
regression tests, including the foreground fake with round one proved and round two flaky.
