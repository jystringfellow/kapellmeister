# Decisions

One file per decision that constrains future work, named `YYYY-MM-DD-what-was-decided.md`.

- **When to add one:** a later change could reasonably undo the choice without knowing why it was
  made, such as an approach that was tried and rejected, or a rule about who may set what. The
  reason for a single change goes in its commit and pull request instead.
- **What it holds:** the decision as a title, why, what was rejected, and the evidence.
- **Replacing one:** only with new evidence. Add the new file and delete the one it replaces in the
  same change; say in the new file what changed.
- **This file is not an index.** Do not list decisions here; list the directory.
