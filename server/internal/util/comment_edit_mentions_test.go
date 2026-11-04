package util

import (
	"reflect"
	"testing"
)

func TestCommentEditMentions(t *testing.T) {
	denny := "[@Denny](mention://member/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa)"
	li := "[@李群](mention://member/bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb)"
	a := Mention{Type: "member", ID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}
	b := Mention{Type: "member", ID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"}
	for _, tt := range []struct {
		name, before, after string
		want                []Mention
	}{
		{"adds recipient twice", denny + " 选择方案", denny + " " + li + " 选择方案 " + li, []Mention{b}},
		{"adds recipient and changes body", denny + " 选择方案", denny + " " + li + " 选择方案 B " + li, []Mention{a, b}},
		{"changes body", denny + " 方案 A", denny + " 方案 B", []Mention{a}},
		{"removes recipient", denny + " " + li + " 方案 A", denny + " 方案 A", nil},
		{"removed recipient stays excluded on body edit", denny + " " + li + " 方案 A", denny + " 方案 B", []Mention{a}},
		{"unchanged", denny + " 方案 A", denny + " 方案 A", nil},
		{"duplicate mention only", denny + " 方案 A", denny + " 方案 A " + denny, nil},
		{"whitespace only", denny + " 方案 A", "  " + denny + "   方案 A \n", nil},
		{"mention on separate line", denny + "\n方案 A", denny + "\n" + li + "\n方案 A", []Mention{b}},
		{"paragraph boundary edit", denny + "\n方案 A\n方案 B", denny + "\n方案 A\n\n方案 B", []Mention{a}},
		{"code indentation edit", denny + "\n```python\nif x:\n    run()\n```", denny + "\n```python\nif x:\nrun()\n```", []Mention{a}},
		{"inline code spacing edit", denny + " `a  b`", denny + " `a b`", []Mention{a}},
		{"mention label change", denny + " 方案 A", "[@Denny Liu](mention://member/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa) 方案 A", nil},
		{"link edit", denny + " [方案](https://a.test)", denny + " [方案](https://b.test)", []Mention{a}},
		{"issue reference edit", denny + " [LC-1](mention://issue/cccccccc-cccc-cccc-cccc-cccccccccccc)", denny + " [LC-2](mention://issue/dddddddd-dddd-dddd-dddd-dddddddddddd)", []Mention{a}},
		{"no remaining recipients", denny + " 方案 A", "方案 B", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := CommentEditMentions(tt.before, tt.after); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("recipients = %+v, want %+v", got, tt.want)
			}
		})
	}
}
