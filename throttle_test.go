package gulp

import (
	"io/fs"
	"reflect"
	"testing"
	"time"
)

func TestThrottleChanges(t *testing.T) {
	reported := map[string]time.Time{}
	first, throttled := throttleChanges([]fsEvent{{name: "change", path: "a"}, {name: "add", path: "b"}}, nil, reported)
	if want := []fsEvent{{name: "change", path: "a"}, {name: "add", path: "b"}}; !reflect.DeepEqual(first, want) {
		t.Fatalf("first scan: got %v, want %v", first, want)
	}
	second, throttled := throttleChanges([]fsEvent{{name: "change", path: "a"}, {name: "change", path: "b"}}, throttled, reported)
	if want := []fsEvent{{name: "change", path: "b"}}; !reflect.DeepEqual(second, want) {
		t.Fatalf("second scan: got %v, want %v", second, want)
	}
	third, _ := throttleChanges([]fsEvent{{name: "change", path: "a"}, {name: "unlink", path: "b"}}, throttled, reported)
	if want := []fsEvent{{name: "change", path: "a"}, {name: "unlink", path: "b"}}; !reflect.DeepEqual(third, want) {
		t.Fatalf("third scan: got %v, want %v", third, want)
	}
}

// modInfo is a FileInfo with only a modification time.
type modInfo struct {
	fs.FileInfo
	mod time.Time
}

func (m modInfo) ModTime() time.Time { return m.mod }

func TestThrottleSameWriteScansApart(t *testing.T) {
	reported := map[string]time.Time{}
	t1, t2 := time.Unix(100, 1), time.Unix(100, 2)
	ev := func(mod time.Time) []fsEvent { return []fsEvent{{name: "change", path: "a", info: modInfo{mod: mod}}} }
	if got, _ := throttleChanges(ev(t1), nil, reported); len(got) != 1 {
		t.Fatalf("first change dropped: %v", got)
	}
	// the size of the same write seen two scans later: same modification time, not a new change
	if got, _ := throttleChanges(ev(t1), nil, reported); len(got) != 0 {
		t.Fatalf("same write reported twice: %v", got)
	}
	if got, _ := throttleChanges(ev(t2), nil, reported); len(got) != 1 {
		t.Fatalf("a new write dropped: %v", got)
	}
	// after an unlink the next change of the path is reported whatever its time
	throttleChanges([]fsEvent{{name: "unlink", path: "a"}}, nil, reported)
	if got, _ := throttleChanges(ev(t2), nil, reported); len(got) != 1 {
		t.Fatalf("change after unlink dropped: %v", got)
	}
}
