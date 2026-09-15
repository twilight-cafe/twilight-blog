package service

import "fmt"

// ValidationError 标记「由客户端输入引起」的失败，而非服务端或数据库问题。
// handler 可以据此直接应答 400 而不是 500，无需去匹配错误字符串。
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

// invalidf 构造一个带格式化消息的 ValidationError。
func invalidf(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}
