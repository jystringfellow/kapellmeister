# A seat call that times out is not retried

When the call that seats a preflight chair hits its own deadline, the chair fails on that attempt.
Chairs are seated one at a time and a launch is bounded at three-eighths of `preflight_timeout`,
so a retry makes every other chair wait a second full bound, and the first launch may still be
running when the second starts. Seat calls that fail with a server error are retried, and a check
that times out gets one fresh chair, because those end with the session's state known.

Tried and rejected: one retry of a timed-out seat call. It was written to fix a chair that
refused with "policy is still loading, try again" and passed four minutes later. That failure
does not reach the seat call at all: the chair is seated and the check fails fast. The retry was
reverted, and the fast failure is handled by waiting `[run] preflight_retry_wait` before the
retry a failed check already had.

The rule was held only by tests and the README before this, with no reason written down, which
is how it came to be reversed once. Evidence: `TestPreflightDoesNotRetryASeatWhoseCAOCallTimedOut`,
`TestPreflightRetriesOnlyACheckWhoseDeadlineErrorIsWrapped`,
`TestARefusalThatSaysToWaitAndTryAgainIsNotABlocker` and
`TestPreflightWaitsBeforeItsOneRetryWhenACheckFailsFastWithNoBlockerOnScreen`.
