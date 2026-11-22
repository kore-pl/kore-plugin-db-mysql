package mysqlop

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
)

// TestDecodeBatchField NULL、空串、制表符/换行/反斜杠转义。
func TestDecodeBatchField(t *testing.T) {
	cases := []struct {
		name    string
		field   string
		escaped bool
		wantNul bool
		want    string
	}{
		{"NULL", "NULL", true, true, ""},
		{"反斜杠N", `\N`, true, true, ""},
		{"空串", "", true, false, ""},
		{"字段内制表符", `a\tb`, true, false, "a\tb"},
		{"字段内换行", `x\ny`, true, false, "x\ny"},
		{"字段内反斜杠", `p\\q`, true, false, `p\q`},
		{"raw 模式不还原转义", `a\tb`, false, false, `a\tb`},
		{"普通文本", "café", true, false, "café"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decodeBatchField(c.field, c.escaped)
			if got.isNull != c.wantNul {
				t.Fatalf("isNull=%v，期望 %v", got.isNull, c.wantNul)
			}
			if !c.wantNul && got.text != c.want {
				t.Fatalf("text=%q，期望 %q", got.text, c.want)
			}
		})
	}
}

// TestUnescapeBatch 未知转义保留原样，已知转义还原。
func TestUnescapeBatch(t *testing.T) {
	cases := map[string]string{
		`a\tb`:  "a\tb",
		`a\nb`:  "a\nb",
		`a\\b`:  `a\b`,
		`a\0b`:  "a\x00b",
		`a\xb`:  `a\xb`,
		`plain`: "plain",
	}
	for in, want := range cases {
		if got := unescapeBatch(in); got != want {
			t.Fatalf("unescapeBatch(%q)=%q，期望 %q", in, got, want)
		}
	}
}

// TestFieldJSON 数字推断只接受规范写法，前导零保留字符串。
func TestFieldJSON(t *testing.T) {
	cases := map[string]string{
		"42":   "42",
		"-7":   "-7",
		"3.14": "3.14",
		"1e3":  "1e3",
		"007":  `"007"`,
		"-0.5": "-0.5",
		"abc":  `"abc"`,
		"":     `""`,
		`p\q`:  `"p\\q"`,
	}
	for in, want := range cases {
		if got := string(fieldJSON(decodeBatchField(in, false))); got != want {
			t.Fatalf("fieldJSON(%q)=%s，期望 %s", in, got, want)
		}
	}
	if got := string(fieldJSON(batchField{isNull: true})); got != "null" {
		t.Fatalf("NULL 字段编码=%s，期望 null", got)
	}
}

// TestBuildRow 列顺序 + NULL + 转义字段 + 列的重复名归一。
func TestBuildRow(t *testing.T) {
	columns := []string{"n", "s", "n_2"}
	row, err := buildRow(columns, []batchField{
		{isNull: true},
		decodeBatchField(`a\tb`, true),
		decodeBatchField("007", true),
	})
	if err != nil {
		t.Fatalf("buildRow 失败: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(row, &decoded); err != nil {
		t.Fatalf("行 JSON 非法: %v (%s)", err, row)
	}
	if decoded["n"] != nil {
		t.Fatalf("n=%v，期望 null", decoded["n"])
	}
	if decoded["s"] != "a\tb" {
		t.Fatalf("s=%q，期望含制表符的字段", decoded["s"])
	}
	if decoded["n_2"] != "007" {
		t.Fatalf("n_2=%v，期望字符串 007", decoded["n_2"])
	}
}

// TestBuildRowPadsMissing 字段少于列数时缺失列补 null。
func TestBuildRowPadsMissing(t *testing.T) {
	row, err := buildRow([]string{"a", "b"}, []batchField{decodeBatchField("1", true)})
	if err != nil {
		t.Fatalf("buildRow 失败: %v", err)
	}
	if string(row) != `{"a":1,"b":null}` {
		t.Fatalf("row=%s", row)
	}
}

// TestNormalizeColumns 空列名与重名列归一。
func TestNormalizeColumns(t *testing.T) {
	got := normalizeColumns([]string{"id", "", "id", "id_2"})
	want := []string{"id", "column_2", "id_2", "id_2_2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("normalizeColumns=%v，期望 %v", got, want)
	}
}

// TestParseTSV --batch 输出解析：NULL、转义制表符/换行、空字段。
func TestParseTSV(t *testing.T) {
	data := []byte("NULL\ta\\tb\tp\\\\q\n42\tx\\ny\t\n")
	rows, err := parseTSV(data, true)
	if err != nil {
		t.Fatalf("parseTSV 失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("行数=%d，期望 2（%v）", len(rows), rows)
	}
	if rows[0][0] != "NULL" || rows[0][1] != "a\tb" || rows[0][2] != `p\q` {
		t.Fatalf("首行解析错: %q", rows[0])
	}
	if rows[1][0] != "42" || rows[1][1] != "x\ny" || rows[1][2] != "" {
		t.Fatalf("次行解析错: %q", rows[1])
	}
}

// TestParseTSVRaw raw 模式不还原转义。
func TestParseTSVRaw(t *testing.T) {
	rows, err := parseTSV([]byte("a\tb\tc\n"), false)
	if err != nil {
		t.Fatalf("parseTSV 失败: %v", err)
	}
	if len(rows) != 1 || rows[0][1] != "b" || rows[0][2] != "c" {
		t.Fatalf("raw 解析错: %v", rows)
	}
}

// TestReadBatchLineTooLong 单行超过上限返回 errLineTooLong。
func TestReadBatchLineTooLong(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 64)+"\n"), 16)
	if _, err := readBatchLine(reader, 32); err == nil {
		t.Fatal("超长行未报错")
	}
}
