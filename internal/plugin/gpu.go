// GPUPlugin 通过解析 nvidia-smi 命令输出采集 GPU 指标
package plugin

import (
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// GPUPlugin 采集 NVIDIA GPU 利用率、显存和温度数据
// 若系统未安装 nvidia-smi，Collect() 静默返回空切片
type GPUPlugin struct {
	available bool // nvidia-smi 是否可用（由 Init 探测）
}

// NewGPUPlugin 创建一个新的 GPU 插件
func NewGPUPlugin() *GPUPlugin {
	return &GPUPlugin{}
}

func (p *GPUPlugin) Name() string    { return "gpu" }
func (p *GPUPlugin) Version() string { return "1.0.0" }

// Init 探测 nvidia-smi 是否存在；找不到时标记不可用（不返回错误）
func (p *GPUPlugin) Init(_ map[string]string) error {
	_, err := exec.LookPath("nvidia-smi")
	p.available = err == nil
	return nil
}

// Collect 调用 nvidia-smi 获取每卡的利用率/显存/温度
// 命令格式：nvidia-smi --query-gpu=index,utilization.gpu,memory.used,temperature.gpu --format=csv,noheader,nounits
// 输出示例：0, 52, 2048, 65
func (p *GPUPlugin) Collect() []model.MetricPoint {
	if !p.available {
		return nil
	}

	out, err := exec.Command(
		"nvidia-smi",
		"--query-gpu=index,utilization.gpu,memory.used,temperature.gpu",
		"--format=csv,noheader,nounits",
	).Output()
	if err != nil {
		return nil
	}

	now := time.Now().UnixMilli()
	var points []model.MetricPoint

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 4 {
			continue
		}
		idx := strings.TrimSpace(parts[0])
		labels := map[string]string{"gpu": idx}

		if v, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64); err == nil {
			points = append(points, model.MetricPoint{Name: "gpu_usage_percent", Labels: labels, Value: v, Timestamp: now})
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64); err == nil {
			points = append(points, model.MetricPoint{Name: "gpu_memory_used_mb", Labels: labels, Value: v, Timestamp: now})
		}
		if v, err := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64); err == nil {
			points = append(points, model.MetricPoint{Name: "gpu_temperature_celsius", Labels: labels, Value: v, Timestamp: now})
		}
	}
	return points
}

func (p *GPUPlugin) Interval() time.Duration { return 15 * time.Second }
func (p *GPUPlugin) Stop()                   {}
