package ensemble

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/kapellmeister/internal/loop"
)

func TestEnsembleDescriptionsAreValidatedInUserAndRepositoryFiles(t *testing.T) {
	for _, portable := range []bool{false, true} {
		for _, value := range []string{"useful", strings.Repeat("x", 512), "", " \n\t", strings.Repeat("x", 513)} {
			path := writeEnsemble(t, t.TempDir(), "sample", fmt.Sprintf("description = %q\n", value))
			spec, err := load("sample", path, portable, loop.BuiltInAgents())
			valid := strings.TrimSpace(value) != "" && len(value) <= 512
			if !valid {
				if err == nil || !strings.Contains(err.Error(), path) {
					t.Fatalf("%q: %v", value, err)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			field := reflect.ValueOf(spec).FieldByName("Description")
			if !field.IsValid() || field.String() != value {
				t.Fatalf("description missing: %+v", spec)
			}
		}
	}
}
