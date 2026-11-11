package gulp

import (
	"reflect"
	"testing"
)

func TestThrottleChanges(t *testing.T) {
	first, throttled := throttleChanges([]fsEvent{{name: "change", path: "a"}, {name: "add", path: "b"}}, nil)
	if want := []fsEvent{{name: "change", path: "a"}, {name: "add", path: "b"}}; !reflect.DeepEqual(first, want) {
		t.Fatalf("first scan: got %v, want %v", first, want)
	}
	second, throttled := throttleChanges([]fsEvent{{name: "change", path: "a"}, {name: "change", path: "b"}}, throttled)
	if want := []fsEvent{{name: "change", path: "b"}}; !reflect.DeepEqual(second, want) {
		t.Fatalf("second scan: got %v, want %v", second, want)
	}
	third, _ := throttleChanges([]fsEvent{{name: "change", path: "a"}, {name: "unlink", path: "b"}}, throttled)
	if want := []fsEvent{{name: "change", path: "a"}, {name: "unlink", path: "b"}}; !reflect.DeepEqual(third, want) {
		t.Fatalf("third scan: got %v, want %v", third, want)
	}
}
