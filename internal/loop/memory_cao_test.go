package loop

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/cao"
)

func TestOnlyARefusalBeforeTheReusedStepWasAcceptedFallsBackToFresh(t *testing.T) {
	for _, tc := range []struct {
		name        string
		patch, post int
		refused     bool
	}{
		{"missing label", 404, 200, true}, {"unavailable label", 503, 200, true}, {"missing terminal", 200, 404, true}, {"invalid request", 200, 422, true}, {"step timeout", 200, 504, false}, {"server failure", 200, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status := tc.post
				if r.Method == http.MethodPatch {
					status = tc.patch
				}
				w.WriteHeader(status)
				if status == 504 {
					w.Write([]byte(`{"detail":{"kind":"timeout","terminal_id":"1234abcd"}}`))
				} else {
					w.Write([]byte(`{"detail":"Terminal '1234abcd' not found"}`))
				}
			}))
			defer srv.Close()
			_, err := (CAOStepper{Client: cao.NewClient(srv.URL)}).Run(context.Background(), Step{ReuseTerminal: "1234abcd"})
			if errors.Is(err, ErrReuseRefused) != tc.refused {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestAReuseRefusalPreservesTheServersMessageAndOriginalError(t *testing.T) {
	original := &cao.HTTPError{Status: 404, Body: "missing"}
	err := reuseRefusedError{original}
	var httpErr *cao.HTTPError
	if err.Error() != original.Error() || !errors.Is(err, ErrReuseRefused) || !errors.As(err, &httpErr) || httpErr != original {
		t.Fatal(err)
	}
}
