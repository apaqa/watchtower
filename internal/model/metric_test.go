// Package model 单元测试：验证指纹生成的一致性和正确性
package model

import (
	"testing"
)

// TestFingerprintConsistency 验证相同名称和标签总是生成相同指纹
func TestFingerprintConsistency(t *testing.T) {
	labels := map[string]string{"host": "node1", "env": "prod"}

	fp1 := Fingerprint("cpu_usage_percent", labels)
	fp2 := Fingerprint("cpu_usage_percent", labels)

	if fp1 != fp2 {
		t.Errorf("相同输入应生成相同指纹，fp1=%s fp2=%s", fp1, fp2)
	}
}

// TestFingerprintLabelOrder 验证标签顺序不影响指纹结果
func TestFingerprintLabelOrder(t *testing.T) {
	labels1 := map[string]string{"host": "node1", "env": "prod", "region": "us-east"}
	labels2 := map[string]string{"region": "us-east", "host": "node1", "env": "prod"}

	fp1 := Fingerprint("metric", labels1)
	fp2 := Fingerprint("metric", labels2)

	if fp1 != fp2 {
		t.Errorf("标签顺序不同时应生成相同指纹，fp1=%s fp2=%s", fp1, fp2)
	}
}

// TestFingerprintDifferentNames 验证不同指标名称生成不同指纹
func TestFingerprintDifferentNames(t *testing.T) {
	labels := map[string]string{"host": "node1"}

	fp1 := Fingerprint("cpu_usage_percent", labels)
	fp2 := Fingerprint("memory_usage_percent", labels)

	if fp1 == fp2 {
		t.Error("不同指标名称应生成不同指纹")
	}
}

// TestFingerprintDifferentLabels 验证不同标签值生成不同指纹
func TestFingerprintDifferentLabels(t *testing.T) {
	fp1 := Fingerprint("cpu", map[string]string{"host": "node1"})
	fp2 := Fingerprint("cpu", map[string]string{"host": "node2"})

	if fp1 == fp2 {
		t.Error("不同标签值应生成不同指纹")
	}
}

// TestFingerprintEmptyLabels 验证空标签集合能正常生成指纹
func TestFingerprintEmptyLabels(t *testing.T) {
	fp := Fingerprint("metric", map[string]string{})
	if fp == "" {
		t.Error("空标签集合应生成非空指纹")
	}
}
