# Own less; use CAO's primitives and upstream the gaps

kapellmeister owns only what CAO does not provide. Where CAO has a primitive (sessions, terminals,
group labels, siblings, the inbox, `send_message`), km uses it, even when it has known gaps. What
km must own goes behind a narrow seam shaped like the CAO primitive it stands in for, so it can be
swapped out when CAO ships one, and the gap becomes a proposed contribution to CAO. Workarounds for
CAO bugs live in the CAO stepper and are removed once the fix ships. This scopes the 2026-11-03
decision: km keeps its own loop, the score, but not its own team plumbing. Evidence: the
ensemble spike in `spike/SPIKE.md`, which found CAO covers boot, sections and sibling discovery,
and lacks acknowledged messages, an operator mailbox and restart recovery.
