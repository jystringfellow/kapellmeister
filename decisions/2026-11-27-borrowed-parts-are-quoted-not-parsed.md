# Borrowed parts are quoted, not parsed

A borrowed file is written for another agent, not for km. Treat its body as quoted text: do not
substitute placeholders or split step headings, and do not interpret its front matter.

We rejected applying km's part-file parsing and agent settings to the borrowed file. Existing agent
definitions can contain literal braces and headings, and interpreting front matter would suggest
that its tools or model settings govern the chair.
