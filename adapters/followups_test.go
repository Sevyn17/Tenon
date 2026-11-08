package adapters

import (
	"reflect"
	"testing"
)

func TestParseFollowups(t *testing.T) {
	for in, want := range map[string][]string{
		`["再详细点","举个例子"]`:                                 {"再详细点", "举个例子"},
		"好的：\n```json\n[\"a\", \"a\", \" \", \"b\"]\n```": {"a", "b"},
		`[]`:                    {},
		`没有 JSON`:               {},
		`["1","2","3","4","5"]`: {"1", "2", "3", "4"},
		`[1, 2]`:                {},
	} {
		if got := parseFollowups(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q → %q，期望 %q", in, got, want)
		}
	}
}
