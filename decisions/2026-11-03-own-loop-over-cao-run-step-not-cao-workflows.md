# Own loop over CAO run-step, not CAO workflows

kapellmeister drives plan, implement, review and gate itself through `/terminals/run-step`. CAO
workflows were tried twice and rejected: every new worktree needs `cao workflow approve`, script
workflows have no `workflow_return`, and runs record almost no telemetry. Rechecked against CAO
main on 2026-11-04. Evidence: `spike/SPIKE.md`, findings 9, 10, 12 and 13.
