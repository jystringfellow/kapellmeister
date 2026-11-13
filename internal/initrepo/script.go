package initrepo

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
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
 sh -c "$2" >"$log" 2>&1
 status=$?
 if [ "$status" -ne 0 ]; then
  echo "verify failed at step: $1 (exit $status)"
  echo "--- last 40 lines of $1 ---"
  tail -n 40 "$log" | cut -c1-300
  echo "full log: $log"
  exit "$status"
 fi
}

`

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
