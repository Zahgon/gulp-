package gulp

import (
	"reflect"
	"testing"
)

func TestThrottleChanges(t *testing.T) {
	first, throttled := throttleChanges([]fsEvent{{"change", "a"}, {"add", "b"}}, nil)
	if want := []fsEvent{{"change", "a"}, {"add", "b"}}; !reflect.DeepEqual(first, want) {
		t.Fatalf("first scan: got %v, want %v", first, want)
	}
	second, throttled := throttleChanges([]fsEvent{{"change", "a"}, {"change", "b"}}, throttled)
	if want := []fsEvent{{"change", "b"}}; !reflect.DeepEqual(second, want) {
		t.Fatalf("second scan: got %v, want %v", second, want)
	}
	third, _ := throttleChanges([]fsEvent{{"change", "a"}, {"unlink", "b"}}, throttled)
	if want := []fsEvent{{"change", "a"}, {"unlink", "b"}}; !reflect.DeepEqual(third, want) {
		t.Fatalf("third scan: got %v, want %v", third, want)
	}
}
