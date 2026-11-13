package initrepo

import (
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

func RenderScript(steps []Step) (string, error) {
	var b strings.Builder
	b.WriteString(`#!/bin/sh
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

`)
	labels := []string{}
	seen := map[string]bool{}
	for _, step := range steps {
		if err := step.validate(); err != nil {
			return "", err
		}
		if seen[step.Label] {
			return "", fmt.Errorf("duplicate step label: %s", step.Label)
		}
		seen[step.Label] = true
		fmt.Fprintf(&b, "run '%s' '%s'\n", step.Label, step.Command)
		labels = append(labels, step.Label)
	}
	fmt.Fprintf(&b, "rm -f \"$log\"\nprintf '%%s\\n' 'verify ok: %s'\n", strings.Join(labels, ", "))
	return b.String(), nil
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
