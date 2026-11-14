# Tune verify omits a test only for an observed reason

A Go repository's whole suite passed in about 20 seconds, yet tune approved in one round a
verify skipping 38 tests by name because they used sleeps, scheduling deadlines or the clock.
None had failed in either clean run; together they covered a whole feature, leaving changes to
that feature unverified. The whole suite then proved first time at about 25 seconds per clean run.

The shared guide now requires the whole suite when it fits. Each omission names one of three
reasons with evidence: a failure or hang during this tune (which run and its output), something
the clean copy cannot have (network, a service, a credential, another machine), or a cut because
the suite exceeds the target (measured time). Suspicion from reading source is not a reason;
the reviewer checks evidence and asks for changes for unsupported omissions or a suite that fits.
The 2026-11-13 large-repository guidance still holds: about two minutes, never over five, fast
CI checks plus a subset when CI is longer, and a flaky verify is worse than a narrower one.
The by-hand command remains required. TuneOmission and km prove are unchanged.
