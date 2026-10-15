package agent

import "testing"

func TestRepairJSONCapitalizedBooleans(t *testing.T) {
	in := `{"summary": "ok", "confidence": True, "done": False}`
	want := `{"summary": "ok", "confidence": true, "done": false}`

	if got := repairJSON(in); got != want {
		t.Fatalf("repairJSON(%q) = %q, want %q", in, got, want)
	}
}

func TestRepairJSONNoneToNull(t *testing.T) {
	for _, value := range []string{"None", "Null", "NULL"} {
		in := `{"field": ` + value + `}`
		if got := repairJSON(in); got != `{"field": null}` {
			t.Fatalf("repairJSON(%q) = %q, want null", in, got)
		}
	}
}

func TestRepairJSONTrailingCommas(t *testing.T) {
	for _, test := range []struct{ in, want string }{
		{`{"a": 1,}`, `{"a": 1}`},
		{`{"a": [1, 2,]}`, `{"a": [1, 2]}`},
		{`{"a": {"b": 1,},}`, `{"a": {"b": 1}}`},
	} {
		if got := repairJSON(test.in); got != test.want {
			t.Fatalf("repairJSON(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestRepairJSONDoesNotTouchStrings(t *testing.T) {
	in := `{"summary": "says True and None here", "ok": true}`
	if got := repairJSON(in); got != in {
		t.Fatalf("repairJSON should not modify string content, got %q", got)
	}
}

func TestRepairJSONNoChange(t *testing.T) {
	in := `{"summary": "ok", "hypotheses": ["a", "b"]}`
	if got := repairJSON(in); got != in {
		t.Fatalf("repairJSON should return input unchanged, got %q", got)
	}
}
