package prove

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestTheNoNetworkMethodIsARealIsolationWhenItsProbePassesAndTheLabelledFallbackOtherwise(t *testing.T) {
	method := NoNetworkMethod(context.Background())
	if method != MethodSandbox && method != MethodNamespace && method != MethodProxy {
		t.Fatal(method)
	}
	if method != MethodProxy && exec.Command("sh", "-c", withoutNetwork(method, "true")).Run() != nil {
		t.Fatal(method)
	}
	if !strings.HasSuffix(MethodProxy, "(best effort)") {
		t.Fatal(MethodProxy)
	}
}

func TestTheProxyFallbackPointsEveryProxyVariableAtALocalPortAndExemptsOnlyLoopback(t *testing.T) {
	out, err := exec.Command("sh", "-c", withoutNetwork(MethodProxy, `echo "$http_proxy $HTTPS_PROXY $ALL_PROXY [$no_proxy|$NO_PROXY]"`)).Output()
	fields := strings.Fields(string(out))
	if err != nil || len(fields) != 4 || fields[0] != fields[1] || fields[1] != fields[2] || !strings.HasPrefix(fields[0], "http://127.0.0.1:") || fields[3] != "[localhost,127.0.0.1,::1|localhost,127.0.0.1,::1]" {
		t.Fatal(string(out), err)
	}
}

func TestEvidenceIsTheExitStatusAndTheLastLineTheFailedStepPrinted(t *testing.T) {
	output := "verify failed at step: e2e (exit 7)\n--- last 40 lines of e2e ---\nconnecting\ncurl: (7) could not connect\n\nfull log: /tmp/x\n"
	if got := offlineEvidence(output); got != "exit 7: curl: (7) could not connect" {
		t.Fatal(got)
	}
	if got := offlineEvidence("verify failed at step: e2e (exit 3)\n--- last 40 lines of e2e ---\nfull log: /tmp/x\n"); got != "exit 3" {
		t.Fatal(got)
	}
}
