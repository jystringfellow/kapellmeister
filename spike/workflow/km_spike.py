import json
import random
import subprocess
from pathlib import Path

from cao_workflow import emit_output, get_inputs, step

SEATS = [
    ("claude_code", "km-spike-claude"),
    ("codex", "km-spike-codex"),
    ("copilot_cli", "km-spike-copilot-haiku"),
]

INPUTS = {
    "worktree": {"type": "path", "required": True},
    "seed": {"type": "int", "required": False, "default": 1},
}

inputs = get_inputs()
worktree = Path(inputs["worktree"])
seed = int(inputs.get("seed", "1"))
rng = random.Random(seed)
provider, profile = rng.choice(SEATS)

verify = subprocess.run(["go", "test", "./..."], cwd=worktree, capture_output=True, text=True)

report_path = worktree / ".kapellmeister" / "replies" / f"workflow-review-{seed}.md"
prompt = (
    "Review the most recent commit in this repository (run `git show HEAD`). "
    f"The test suite result was: exit {verify.returncode}.\n"
    "In plain prose, say whether the change is correct and safe to merge, and why. "
    f"Write your full review to {report_path.relative_to(worktree)} and then reply with one short sentence."
)
handle = step(
    provider,
    profile,
    prompt,
    recovery="idempotent",
    step_id=f"review-{seed}",
    working_directory=str(worktree),
)

report = report_path.read_text() if report_path.exists() else None
emit_output(
    {
        "seat": [provider, profile],
        "verify_exit": verify.returncode,
        "status": handle.status,
        "replayed": handle.replayed,
        "captured": handle.output,
        "report_file_present": report is not None,
        "report_chars": len(report or ""),
    }
)
print(json.dumps({"report": report})[:2000])
