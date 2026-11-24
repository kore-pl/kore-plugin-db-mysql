package mysqlop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// OpError 是执行面带协议错误码的错误，classify 优先用它，避免错误信息二次加工时丢码。
type OpError struct {
	Code    string
	Message string
	// cause 是被包装的底层错误（如 *os.PathError{Op:"fork/exec"}、*exec.ExitError、
	// context.DeadlineExceeded）。它不参与 Error() 与任何响应：Message 面向运维，
	// cause 只用于**结构化归因**——探测路径要据此判断「实例不可达」还是「宿主进程
	// 创建失败」，不能靠匹配自己写的中文文案。nil 表示没有底层错误。
	cause error
}

// Error 实现 error 接口。
func (e *OpError) Error() string { return e.Code + ": " + e.Message }

// Unwrap 暴露底层错误：errors.As 可继续向下匹配 *os.PathError / *exec.ExitError，
// 探测路径的归因判定依赖它（语义只加不改：OpError 自身的 Code/Message 不变）。
func (e *OpError) Unwrap() error { return e.cause }

// opErrorf 构造带协议错误码的执行面错误。
func opErrorf(code, format string, args ...any) *OpError {
	return &OpError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// timeoutError 统一超时错误文案（含实际生效超时）。
//
// cause 固定为 context.DeadlineExceeded：调用方据此可结构化判定「按超时归因」，
// 不必匹配「操作超时」这段文案。
func timeoutError(d time.Duration) *OpError {
	op := opErrorf(pluginapi.E_UPSTREAM, "操作超时（%s），已终止 mysql 客户端", d)
	op.cause = context.DeadlineExceeded
	return op
}

// errorText 把执行面错误压成可放进 per-instance error 字段的文本（不含密码）。
func errorText(err error) string {
	var op *OpError
	if errors.As(err, &op) {
		return op.Message
	}
	return err.Error()
}
