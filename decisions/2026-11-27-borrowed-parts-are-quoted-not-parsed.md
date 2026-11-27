# Borrowed parts are quoted, not parsed

A borrowed file is written for another agent, not for km. Treat its body as quoted text: do not
substitute placeholders or split step headings, and do not interpret its front matter. Delimit the
quotation with lines naming the file; km's fixed block stays outside it and last.

We rejected applying km's part-file parsing and agent settings to the borrowed file. Existing agent
definitions can contain literal braces and headings, and interpreting front matter would suggest
that its tools or model settings govern the chair.

A body containing its closing `End of PATH.` line is refused, ignoring surrounding whitespace.
Only that exact line is refused; lookalikes are not detected. We rejected a per-prompt nonce
because it changes prompt hashes, prompt comparisons and native-agent handling on every run,
while a body could still imitate the shape of a delimiter.
