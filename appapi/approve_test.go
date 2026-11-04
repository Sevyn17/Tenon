package appapi

import "testing"

func TestApproveWord(t *testing.T) {
	for in, want := range map[string]string{"YES": "approve", "同意": "approve", "批准": "approve", "no": "deny", "": "deny",
		`{"approve":true}`: `{"approve":true}`} {
		if got := ApproveWord(in); got != want {
			t.Fatalf("ApproveWord(%q)=%q，期望 %q", in, got, want)
		}
	}
}
