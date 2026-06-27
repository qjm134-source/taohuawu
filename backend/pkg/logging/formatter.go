package logging

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
)

// CustomFormatter 自定义日志格式
// 输出格式: 2026-06-27 10:06:09 [info] Starting server... port=8080
type CustomFormatter struct {
	TimestampFormat string
}

func (f *CustomFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	timestamp := entry.Time.Format(f.TimestampFormat)
	level := strings.ToUpper(entry.Level.String())

	// 拼接 fields
	var fields string
	if len(entry.Data) > 0 {
		keys := make([]string, 0, len(entry.Data))
		for k := range entry.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%v", k, entry.Data[k]))
		}
		fields = " " + strings.Join(parts, " ")
	}

	msg := entry.Message
	if len(msg) > 0 && msg[0] == ' ' {
		msg = msg[1:]
	}

	return []byte(fmt.Sprintf("%s [%s] %s%s\n", timestamp, level, msg, fields)), nil
}
