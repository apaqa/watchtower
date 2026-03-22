// Package model 定义 WatchTower 平台的共享数据类型
package model

import (
	"crypto/md5"
	"fmt"
	"sort"
	"strings"
)

// MetricPoint 表示一个入站指标数据点，由采集代理或外部系统通过 HTTP POST 发送
type MetricPoint struct {
	Name      string            `json:"name"`      // 指标名称，如 cpu_usage_percent
	Labels    map[string]string `json:"labels"`    // 标签键值对，如 {host: "node1"}
	Value     float64           `json:"value"`     // 数值
	Timestamp int64             `json:"timestamp"` // Unix 毫秒时间戳；为 0 则由服务器填充
}

// DataPoint 是存储在时间序列中的单个带时间戳的值
type DataPoint struct {
	Timestamp int64   `json:"timestamp"` // Unix 毫秒
	Value     float64 `json:"value"`
}

// Fingerprint 根据指标名称和有序标签生成唯一标识字符串，用于在 TSDB 中索引序列
func Fingerprint(name string, labels map[string]string) string {
	// 对标签键排序，确保相同标签集合生成相同指纹
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteString(name)
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(labels[k])
	}
	sb.WriteByte('}')

	raw := sb.String()
	hash := md5.Sum([]byte(raw))
	return fmt.Sprintf("%x", hash)
}
