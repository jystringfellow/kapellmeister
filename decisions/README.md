# Decisions

One file per decision that constrains future work, named `YYYY-MM-DD-what-was-decided.md`.

- **When to add one:** only for a choice no test can fail on: something we chose not to build, an
  approach that was tried and rejected, or a rule about the work itself. Behaviour is held by its
  tests, and the reason for it by the commit that added them; do not repeat either here.
- **What it holds:** the decision as a title, why, what was rejected, and the evidence.
- **Replacing one:** only with new evidence. Add the new file and delete the one it replaces in the
  same change; say in the new file what changed.
- **This file is not an index.** Do not list decisions here; list the directory.
