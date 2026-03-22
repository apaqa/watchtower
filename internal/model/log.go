// Package model 定义 WatchTower 平台的共享数据类型
package model

import (
	"crypto/md5"
	"fmt"
)

// LogLevel 是日志级别的枚举类型
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// LogEntry 表示一条日志记录
type LogEntry struct {
	Timestamp int64             `json:"timestamp"`        // Unix 毫秒时间戳；为 0 则由服务器填充
	Level     LogLevel          `json:"level"`            // 日志级别：debug/info/warn/error
	Source    string            `json:"source"`           // 来源，如主机名或应用名
	Message   string            `json:"message"`          // 日志消息内容
	Labels    map[string]string `json:"labels,omitempty"` // 附加标签键值对
}

// LogFingerprint 根据来源和级别生成唯一标识字符串，用于日志分组
func LogFingerprint(source string, level LogLevel) string {
	raw := source + ":" + string(level)
	hash := md5.Sum([]byte(raw))
	return fmt.Sprintf("%x", hash)
}
