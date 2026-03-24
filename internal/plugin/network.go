// NetworkPlugin 使用 gopsutil 采集每个网络接口的流量和错误统计
package plugin

import (
	"strings"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	psnet "github.com/shirou/gopsutil/v3/net"
)

// NetworkPlugin 采集网络接口 IO 统计数据
type NetworkPlugin struct {
	// interfaces 指定要采集的接口名称列表；为空则采集所有接口
	interfaces []string
}

// NewNetworkPlugin 创建一个新的网络插件
func NewNetworkPlugin() *NetworkPlugin {
	return &NetworkPlugin{}
}

func (p *NetworkPlugin) Name() string    { return "network" }
func (p *NetworkPlugin) Version() string { return "1.0.0" }

// Init 可选接受 "interfaces" 配置（逗号分隔的接口名，如 "eth0,lo"）
func (p *NetworkPlugin) Init(cfg map[string]string) error {
	if ifaces, ok := cfg["interfaces"]; ok && ifaces != "" {
		parts := strings.Split(ifaces, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		p.interfaces = parts
	}
	return nil
}

// Collect 读取所有（或指定）网络接口的 IO 计数器
func (p *NetworkPlugin) Collect() []model.MetricPoint {
	counters, err := psnet.IOCounters(true) // perNIC=true 返回每个接口的统计
	if err != nil {
		return nil
	}

	now := time.Now().UnixMilli()
	var points []model.MetricPoint

	for _, c := range counters {
		// 若配置了接口过滤则跳过不匹配的接口
		if len(p.interfaces) > 0 && !p.matchInterface(c.Name) {
			continue
		}
		labels := map[string]string{"interface": c.Name}

		points = append(points,
			model.MetricPoint{Name: "network_bytes_sent", Labels: labels, Value: float64(c.BytesSent), Timestamp: now},
			model.MetricPoint{Name: "network_bytes_recv", Labels: labels, Value: float64(c.BytesRecv), Timestamp: now},
			model.MetricPoint{Name: "network_packets_sent", Labels: labels, Value: float64(c.PacketsSent), Timestamp: now},
			model.MetricPoint{Name: "network_packets_recv", Labels: labels, Value: float64(c.PacketsRecv), Timestamp: now},
			model.MetricPoint{Name: "network_errors", Labels: labels,
				Value:     float64(c.Errin + c.Errout),
				Timestamp: now},
		)
	}
	return points
}

// matchInterface 检查接口名是否在允许列表中（大小写不敏感）
func (p *NetworkPlugin) matchInterface(name string) bool {
	for _, iface := range p.interfaces {
		if strings.EqualFold(iface, name) {
			return true
		}
	}
	return false
}

func (p *NetworkPlugin) Interval() time.Duration { return 15 * time.Second }
func (p *NetworkPlugin) Stop()                   {}
