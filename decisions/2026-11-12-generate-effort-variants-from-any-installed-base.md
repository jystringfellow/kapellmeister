# Generate effort variants from any installed base, protecting existing profiles

An isolated CAO 2.5.4 installed `my-reviewer-high` from the user-named `my-reviewer` base and exposed
its permissions and effort through the profile endpoint; missing bases returned 404. It refused a
name installed from another source filename but allowed the matching filename; the refusal printed
an error while exiting zero. CAO's filename check alone cannot protect hand-installed variants. km synchronizes matching-provider effort variants
from any installed base, checks its generated description before replacing a stale installed variant,
and retains the CAO home proof and live-run guards for every pending installation. Colliding variant
names from different providers are refused. Variants inherit permissions only (`permissionMode`,
`allowedTools`, `codexProfile`); the prompt remains km's chair instructions, and MCP, skills and other
base fields are not copied. `inherit` uses the base without reading or installing variants. Evidence:
`TestAUserNamedProfileGetsItsEffortVariantInstalledFromItsBase`,
`TestAVariantKapellmeisterDidNotGenerateIsNotReplaced`,
`TestACustomBasesVariantReplacementIsRefusedWhileAnotherRunIsAlive`,
`TestACustomBasesVariantIsNotInstalledWhenTheServerHomeDiffers` and
`TestAUserDefinedAgentIsSeatedAtATieredChairWithoutAnyKmProfile`.
Keep profile generation behind its narrow seam; effort on CAO run-step and installation from supplied
content are upstream gaps that would let km own less, following the 2026-11-05 decision.
