package initrepo

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

const StubMarker = ": km-unfinished-verify"

func IsStub(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == StubMarker {
			return true
		}
	}
	return false
}

const scriptPrelude = `#!/bin/sh
cd "$(dirname "$0")/.." || exit 1
log=$(mktemp "${TMPDIR:-/tmp}/verify.XXXXXX") || exit 1

run() {
 if [ -n "${KAPELLMEISTER_VERIFY_SKIP:-}" ] && grep -qxF -e "$1" "$KAPELLMEISTER_VERIFY_SKIP" 2>/dev/null; then echo "verify skipped step: $1"; return 0; fi
 sh -c "$2" >"$log" 2>&1
 status=$?
 if [ -n "${KAPELLMEISTER_VERIFY_STEPS:-}" ]; then printf '%s\n' "$1" >>"$KAPELLMEISTER_VERIFY_STEPS"; fi
 if [ "$status" -ne 0 ]; then
  if [ -n "${KAPELLMEISTER_VERIFY_STEPS:-}" ]; then printf '%s\n' "$1" >"$KAPELLMEISTER_VERIFY_STEPS.failed"; fi
  echo "verify failed at step: $1 (exit $status)"
  echo "--- last 40 lines of $1 ---"
  tail -n 40 "$log" | cut -c1-300
  echo "full log: $log"
  exit "$status"
 fi
}

known_failure() {
 sh -c "$2" >"$log" 2>&1
 status=$?
 outcome=failed
 if [ "$status" -eq 0 ]; then outcome=passed; fi
 if [ -n "${KAPELLMEISTER_VERIFY_STEPS:-}" ]; then
  printf '%s\n' "$1" >>"$KAPELLMEISTER_VERIFY_STEPS"
  printf '%s %s\n' "$outcome" "$1" >>"$KAPELLMEISTER_VERIFY_STEPS.known"
 fi
 if [ "$status" -eq 0 ]; then echo "known failure passed: $1"; return 0; fi
 echo "known failure failed: $1 (exit $status)"
 echo "--- last 40 lines of $1 ---"
 tail -n 40 "$log" | cut -c1-300
}

`

const StepsEnv = loop.VerifyStepsEnv
const SkipEnv = "KAPELLMEISTER_VERIFY_SKIP"

var setsSkip = regexp.MustCompile(SkipEnv + `:?=`)

func SetsSkip(text string) bool {
	return setsSkip.MatchString(text)
}

func RenderStub() string {
	return scriptPrelude + StubMarker + `
rm -f "$log"
printf '%s\n' 'The verify command has not been written.' 'A verify must pass on a clean checkout, fail when the code or a test is broken, and finish in reasonable time.' 'Run km tune verify to write and prove it with your ensemble, or write it by hand and run km prove.' >&2
exit 1
`
}

func RenderVerify(command string) (string, error) {
	if strings.ContainsRune(command, 0) {
		return "", fmt.Errorf("verify command cannot contain NUL")
	}
	return scriptPrelude + "run 'verify' " + shellQuote(command) + "\nrm -f \"$log\"\nprintf '%s\\n' 'verify ok: verify'\n", nil
}

func RenderConfig(verify, check string) string {
	quote := func(value string) string {
		var b strings.Builder
		_ = toml.NewEncoder(&b).Encode(map[string]string{"value": value})
		_, encoded, _ := strings.Cut(b.String(), " = ")
		return strings.TrimSpace(encoded)
	}
	out := "[test]\nverify = " + quote(verify) + "\n"
	if check != "" {
		out += "check  = " + quote(check) + "\n"
	}
	return out
}
