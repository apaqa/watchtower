// DockerPlugin 通过解析 docker stats 命令输出采集容器资源指标
package plugin

import (
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// DockerPlugin 采集每个运行中容器的 CPU/内存占用及状态
// 若系统未安装 docker，Collect() 静默返回空切片
type DockerPlugin struct {
	available bool // docker 命令是否可用（由 Init 探测）
}

// NewDockerPlugin 创建一个新的 Docker 插件
func NewDockerPlugin() *DockerPlugin {
	return &DockerPlugin{}
}

func (p *DockerPlugin) Name() string    { return "docker" }
func (p *DockerPlugin) Version() string { return "1.0.0" }

// Init 探测 docker 命令是否存在；找不到时标记不可用（不返回错误）
func (p *DockerPlugin) Init(_ map[string]string) error {
	_, err := exec.LookPath("docker")
	p.available = err == nil
	return nil
}

// Collect 调用 docker stats --no-stream 采集所有运行中容器的资源使用情况
// 输出格式（--format "{{.Name}},{{.CPUPerc}},{{.MemUsage}},{{.Status}}"）：
//
//	my_app,0.50%,128MiB / 2GiB,Up 5 hours
func (p *DockerPlugin) Collect() []model.MetricPoint {
	if !p.available {
		return nil
	}

	out, err := exec.Command(
		"docker", "stats", "--no-stream",
		"--format", "{{.Name}},{{.CPUPerc}},{{.MemUsage}},{{.Status}}",
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
		// 格式：name,cpu%,mem_used / mem_limit,status
		// 以逗号分割，最多分为 4 段（状态字段可能含逗号，但 docker 格式中通常不含）
		parts := strings.SplitN(line, ",", 4)
		if len(parts) < 4 {
			continue
		}

		name := strings.TrimSpace(parts[0])
		// 去掉容器名开头的 "/" (docker stats 有时包含)
		name = strings.TrimPrefix(name, "/")
		labels := map[string]string{"container": name}

		// 解析 CPU 百分比（如 "0.50%"）
		cpuStr := strings.TrimSpace(strings.TrimSuffix(parts[1], "%"))
		if v, err := strconv.ParseFloat(cpuStr, 64); err == nil {
			points = append(points, model.MetricPoint{
				Name: "container_cpu_percent", Labels: labels, Value: v, Timestamp: now,
			})
		}

		// 解析内存使用量（如 "128MiB / 2GiB"，取第一段 "128MiB"）
		memParts := strings.SplitN(parts[2], "/", 2)
		if len(memParts) >= 1 {
			if mb, ok := parseMemMB(strings.TrimSpace(memParts[0])); ok {
				points = append(points, model.MetricPoint{
					Name: "container_memory_mb", Labels: labels, Value: mb, Timestamp: now,
				})
			}
		}

		// container_status：running=1，其他=0
		status := strings.ToLower(strings.TrimSpace(parts[3]))
		var statusVal float64
		if strings.HasPrefix(status, "up") {
			statusVal = 1
		}
		points = append(points, model.MetricPoint{
			Name: "container_status", Labels: labels, Value: statusVal, Timestamp: now,
		})
	}
	return points
}

// parseMemMB 将 docker 内存字符串（如 "128MiB"、"1.5GiB"、"512MB"）转换为 MiB
func parseMemMB(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "--" {
		return 0, false
	}
	// 常见单位后缀
	units := []struct {
		suffix string
		factor float64
	}{
		{"GiB", 1024},
		{"MiB", 1},
		{"KiB", 1.0 / 1024},
		{"GB", 1000},
		{"MB", 1},
		{"KB", 0.001},
		{"B", 1.0 / (1024 * 1024)},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			numStr := strings.TrimSuffix(s, u.suffix)
			if v, err := strconv.ParseFloat(strings.TrimSpace(numStr), 64); err == nil {
				return v * u.factor, true
			}
		}
	}
	return 0, false
}

func (p *DockerPlugin) Interval() time.Duration { return 30 * time.Second }
func (p *DockerPlugin) Stop()                   {}
