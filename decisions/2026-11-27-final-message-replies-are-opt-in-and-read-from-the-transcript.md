# Final-message replies are opt-in and read from the transcript

This replaces the earlier decision not to read provider transcripts.

Final-message replies remain opt-in, all or nothing, and never a fallback for a
file chair. km reads the chair's own Claude Code transcript, located as the usage
meter locates it, and uses the screen only when no parent transcript grew.

The screen contains only 50 visible rows. A probe with Claude Code 2.1.332 and
CAO 2.5.4 lost replies over about 40 rows and fenced lines beginning with `>`,
`❯`, or `●`. Rules, quotes, and headings were rewritten, tables box-drawn,
fences dropped, and lines over 220 columns hard-wrapped, including JSON strings.
The captures are in the loop test fixtures.

A provider-backed final message in CAO remains the eventual replacement.
