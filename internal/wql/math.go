// Package wql — math.go：向量×向量算术运算扩展
// 基础的标量×标量和向量×标量运算已在 evaluator.go 中实现，
// 本文件补充向量×向量运算，使 WQL 能够支持跨指标的数学表达式，例如：
//   memory_used_bytes / memory_total_bytes * 100
//   (rx_bytes + tx_bytes) / uptime_seconds
package wql

import (
	"fmt"
	"sort"
	"strings"
)

// vectorBinaryOp 对两个向量执行逐元素二元算术运算。
//
// 匹配策略：
//  1. 若右侧向量只有一个元素，则将其广播到左侧所有元素（类似标量）。
//  2. 否则按 canonicalLabelKey 精确匹配标签，丢弃无对应右侧样本的左侧样本。
//
// 参数 op 须为 "+"、"-"、"*"、"/" 之一，由 applyArith 处理。
func vectorBinaryOp(op string, left, right []Sample) ([]Sample, error) {
	if len(right) == 0 {
		return nil, fmt.Errorf("向量运算：右侧向量为空")
	}

	// ── 广播模式：右侧只有一个样本时对所有左侧元素应用 ─────────────────────────
	if len(right) == 1 {
		result := make([]Sample, 0, len(left))
		for _, s := range left {
			v, err := applyArith(op, s.Value, right[0].Value)
			if err != nil {
				return nil, err
			}
			result = append(result, Sample{Labels: copyLabels(s.Labels), Value: v})
		}
		if len(result) == 0 {
			return nil, fmt.Errorf("向量运算：左侧向量为空")
		}
		return result, nil
	}

	// ── 标签匹配模式：按规范化标签键对左右样本配对 ──────────────────────────────
	rightIdx := make(map[string]float64, len(right))
	for _, s := range right {
		rightIdx[canonicalLabelKey(s.Labels)] = s.Value
	}

	var result []Sample
	for _, s := range left {
		key := canonicalLabelKey(s.Labels)
		rightVal, ok := rightIdx[key]
		if !ok {
			continue // 无匹配右侧样本，丢弃（与 PromQL 的 inner join 语义相同）
		}
		v, err := applyArith(op, s.Value, rightVal)
		if err != nil {
			return nil, err
		}
		result = append(result, Sample{Labels: copyLabels(s.Labels), Value: v})
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("向量运算：没有标签完全匹配的样本对")
	}
	return result, nil
}

// canonicalLabelKey 生成标签 map 的规范化字符串表示。
// 通过对键名排序确保相同标签集合总产生同一字符串（用于匹配比较）。
func canonicalLabelKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(labels[k])
		sb.WriteByte('\n')
	}
	return sb.String()
}
