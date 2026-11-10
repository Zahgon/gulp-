package jsnum

import (
	"math"
	"testing"
)

func TestFormat(t *testing.T) {
	tenth, fifth := 0.1, 0.2
	// expectations printed by String(x) in node
	cases := map[float64]string{
		1: "1", 0.1: "0.1", 1e21: "1e+21", 1e-7: "1e-7", 123456789012345680000: "123456789012345680000",
		1.5e-7: "1.5e-7", math.Copysign(0, -1): "0", 0.000001: "0.000001", 1e300: "1e+300",
		9007199254740992: "9007199254740992", 4.35: "4.35", 100: "100", tenth + fifth: "0.30000000000000004",
		-16: "-16", math.NaN(): "NaN", math.Inf(-1): "-Infinity",
	}
	for in, want := range cases {
		if got := Format(in); got != want {
			t.Errorf("Format(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestNumber(t *testing.T) {
	cases := map[string]float64{"0x10": 16, " 12 ": 12, "": 0, "1e3": 1000, "-5": -5, ".5": 0.5, "1.": 1, "-Infinity": math.Inf(-1)}
	for in, want := range cases {
		if got := Number(in); got != want {
			t.Errorf("Number(%q) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"abc", "1e", "--1", "0x", "0xg", "1 2"} {
		if got := Number(in); !math.IsNaN(got) {
			t.Errorf("Number(%q) = %v, want NaN", in, got)
		}
	}
}

func TestParse(t *testing.T) {
	if got := ParseFloat("1.5abc"); got != 1.5 {
		t.Errorf("ParseFloat = %v", got)
	}
	if got := ParseInt(" -42px"); got != -42 {
		t.Errorf("ParseInt = %v", got)
	}
	if !math.IsNaN(ParseInt("px")) || !math.IsNaN(ParseFloat(".")) {
		t.Error("expected NaN")
	}
	if !IsSafeInteger(9007199254740991) || IsSafeInteger(9007199254740992) || IsSafeInteger(1.5) {
		t.Error("IsSafeInteger")
	}
}
