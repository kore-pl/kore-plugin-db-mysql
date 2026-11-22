package mysqlop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// mysql --batch 输出：制表符分隔、每行一条记录、首行是列名。
//
// 关于 --raw：--raw 会关闭字段转义，字段内的制表符/换行会直接破坏 TSV；
// 因此真正承载用户数据（mysql.query）的调用使用默认转义（\t、\n、\\、\0），
// 由本文件还原。元数据（版本、状态）值域受控时才使用 --raw。
const (
	// maxLineBytes 限制单行输出，防止超长字段把内存打爆。
	maxLineBytes = 1 << 20
	// maxResultBytes 限制整次查询结果，低于协议 8 MiB 单帧上限。
	maxResultBytes = 4 << 20
)

// errLineTooLong 表示单行超过上限，调用方据此终止客户端并标记截断/报错。
var errLineTooLong = errors.New("结果单行超过 1 MiB 上限")

// 数字推断只接受规范写法：前导零保留为字符串（如 "007"、"13800138000" 仍按字符串处理）。
var (
	intLiteralRE   = regexp.MustCompile(`^(0|-?[1-9][0-9]*)$`)
	floatLiteralRE = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)
)

// batchField 是解析后的一个 TSV 字段：要么 SQL NULL，要么文本。
type batchField struct {
	isNull bool
	text   string
}

// decodeBatchField 解析一个字段；escaped=true 时还原 mysql 默认转义。
// NULL 判定：客户端把 SQL NULL 输出为文本 "NULL"，部分客户端/配置输出 "\N"，两者都按 NULL 处理。
func decodeBatchField(field string, escaped bool) batchField {
	if field == "NULL" || field == `\N` {
		return batchField{isNull: true}
	}
	if escaped {
		return batchField{text: unescapeBatch(field)}
	}
	return batchField{text: field}
}

// unescapeBatch 还原 mysql --batch 的字段转义。
func unescapeBatch(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case '0':
			b.WriteByte(0)
		case 'b':
			b.WriteByte('\b')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'Z':
			b.WriteByte(26)
		case '\\':
			b.WriteByte('\\')
		case '\'':
			b.WriteByte('\'')
		case '"':
			b.WriteByte('"')
		default:
			// 未知转义保留原样，避免把普通反斜杠吞掉。
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// fieldJSON 把字段编码为 JSON 标量：NULL、数字（规范写法）或字符串。
func fieldJSON(field batchField) json.RawMessage {
	if field.isNull {
		return json.RawMessage("null")
	}
	if intLiteralRE.MatchString(field.text) || floatLiteralRE.MatchString(field.text) {
		return json.RawMessage(field.text)
	}
	data, err := json.Marshal(field.text)
	if err != nil {
		// json.Marshal(string) 不会失败；保底返回 null 而不是 panic。
		return json.RawMessage("null")
	}
	return data
}

// normalizeColumns 补齐空列名并按出现顺序去重（id, id → id, id_2）。
func normalizeColumns(raw []string) []string {
	out := make([]string, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for i, name := range raw {
		if name == "" {
			name = fmt.Sprintf("column_%d", i+1)
		}
		candidate := name
		for suffix := 2; ; suffix++ {
			if _, exists := seen[candidate]; !exists {
				break
			}
			candidate = fmt.Sprintf("%s_%d", name, suffix)
		}
		seen[candidate] = struct{}{}
		out[i] = candidate
	}
	return out
}

// buildRow 按列顺序把字段编码为 JSON 对象；字段少于列数时缺失列补 null。
func buildRow(columns []string, fields []batchField) (json.RawMessage, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, name := range columns {
		if i > 0 {
			b.WriteByte(',')
		}
		nameJSON, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		b.Write(nameJSON)
		b.WriteByte(':')
		if i < len(fields) {
			b.Write(fieldJSON(fields[i]))
		} else {
			b.WriteString("null")
		}
	}
	b.WriteByte('}')
	if !json.Valid(b.Bytes()) {
		return nil, errors.New("行 JSON 编码非法")
	}
	return json.RawMessage(b.Bytes()), nil
}

// readBatchLine 读取一行 batch 输出；带单行上限，避免超长字段撑爆内存。
func readBatchLine(reader *bufio.Reader, max int) (string, error) {
	var buf []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > max {
			return "", errLineTooLong
		}
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(buf) == 0 {
				return "", io.EOF
			}
			break
		}
		return "", err
	}
	if n := len(buf); n > 0 && buf[n-1] == '\n' {
		buf = buf[:n-1]
	}
	if n := len(buf); n > 0 && buf[n-1] == '\r' {
		buf = buf[:n-1]
	}
	return string(buf), nil
}

// parseTSV 把完整 batch 输出解析为行（每行按制表符切分并还原转义）。
// 仅用于元数据与单元测试；用户查询走流式读取以限制内存。
func parseTSV(data []byte, escaped bool) ([][]string, error) {
	text := string(data)
	if strings.HasSuffix(text, "\n") {
		text = text[:len(text)-1]
	}
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	rows := make([][]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		fields := strings.Split(line, "\t")
		row := make([]string, len(fields))
		for i, field := range fields {
			if escaped {
				row[i] = unescapeBatch(field)
			} else {
				row[i] = field
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}
