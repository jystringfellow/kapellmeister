package cao

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestProfilePermissionsReadsOnlyTheLaunchPermissionFields(t *testing.T) {
	for _, tt := range []struct {
		body string
		want ProfilePermissions
	}{
		{`{"permissionMode":"default","allowedTools":["*"],"system_prompt":"ignored"}`, ProfilePermissions{PermissionMode: "default", AllowedTools: []string{"*"}}},
		{`{"codexProfile":"safe","allowedTools":["Read"],"mcpServers":{"canary":"ignored"}}`, ProfilePermissions{CodexProfile: "safe", AllowedTools: []string{"Read"}}},
		{`{"allowedTools":[]}`, ProfilePermissions{AllowedTools: []string{}}},
		{`{}`, ProfilePermissions{}},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" || r.URL.EscapedPath() != "/agents/profiles/a%20profile" {
				t.Errorf("request = %s %s", r.Method, r.URL)
			}
			fmt.Fprint(w, tt.body)
		}))
		got, err := NewClient(srv.URL).ProfilePermissions(context.Background(), "a profile")
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("permissions = %#v, want %#v", got, tt.want)
		}
	}
}

func TestProfilePermissionsReturnsHTTPFailures(t *testing.T) {
	for _, status := range []int{404, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		_, err := NewClient(srv.URL).ProfilePermissions(context.Background(), "missing")
		srv.Close()
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.Status != status {
			t.Fatalf("error = %v, want HTTP %d", err, status)
		}
	}
}
