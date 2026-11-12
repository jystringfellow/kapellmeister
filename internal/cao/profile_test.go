package cao

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateAndDeleteProfileCallTheProfileWriteRoutes(t *testing.T) {
	for _, status := range []int{0, 400, 409} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if status != 0 {
					http.Error(w, "refused", status)
					return
				}
				switch r.Method {
				case "POST":
					if r.URL.Path != "/agents/profiles" {
						t.Error(r.URL.Path)
					}
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["name"] != "km-probe-test" || body["content"] != "profile document" {
						t.Error(body)
					}
					w.WriteHeader(201)
					w.Write([]byte(`{}`))
				case "DELETE":
					if r.URL.Path != "/agents/profiles/km-probe-test" {
						t.Error(r.URL.Path)
					}
					w.WriteHeader(204)
				default:
					t.Error(r.Method)
				}
			}))
			defer srv.Close()
			c := NewClient(srv.URL)
			for _, err := range []error{c.CreateProfile(context.Background(), "km-probe-test", "profile document"), c.DeleteProfile(context.Background(), "km-probe-test")} {
				if status == 0 {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					var h *HTTPError
					if !errors.As(err, &h) || h.Status != status {
						t.Fatalf("error = %v", err)
					}
				}
			}
			if calls != 2 {
				t.Fatal(calls)
			}
		})
	}
}
