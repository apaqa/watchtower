// Package agent collects local system metrics and pushes them to the ingest service.
package agent

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

// Agent periodically collects system metrics and POSTs them to the ingest service.
type Agent struct {
	ingestURL   string        // ingest endpoint, e.g. http://localhost:9090/api/v1/metrics
	registryURL string        // registry endpoint, e.g. http://localhost:9090/api/v1/agents/register
	interval    time.Duration // collection interval
	agentID     string        // stable UUID generated at startup
	hostname    string        // local hostname, used as host label
	ipAddress   string        // outbound IP address
	osName      string        // runtime.GOOS
	arch        string        // runtime.GOARCH
	client      *http.Client
	stopCh      chan struct{}
}

// New creates a new Agent. registryURL may be empty to disable registration.
func New(ingestURL string, interval time.Duration) (*Agent, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	return &Agent{
		ingestURL: ingestURL,
		interval:  interval,
		agentID:   newUUID(),
		hostname:  hostname,
		ipAddress: localIP(),
		osName:    runtime.GOOS,
		arch:      runtime.GOARCH,
		client:    &http.Client{Timeout: 5 * time.Second},
		stopCh:    make(chan struct{}),
	}, nil
}

// SetRegistryURL configures the agent registry endpoint for heartbeats.
func (a *Agent) SetRegistryURL(url string) {
	a.registryURL = url
}

// Start runs the collection loop (blocking; call in a goroutine).
// It also sends an initial registration heartbeat and repeats every 15 seconds.
func (a *Agent) Start() {
	a.register()
	a.collectAndSend()

	collectTicker := time.NewTicker(a.interval)
	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer collectTicker.Stop()
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-collectTicker.C:
			a.collectAndSend()
		case <-heartbeatTicker.C:
			a.register()
		case <-a.stopCh:
			return
		}
	}
}

// Stop halts the collection loop.
func (a *Agent) Stop() {
	close(a.stopCh)
}

// Collect gathers current system metrics and returns a slice of data points.
func (a *Agent) Collect() ([]model.MetricPoint, error) {
	now := time.Now().UnixMilli()
	labels := map[string]string{
		"host":     a.hostname,
		"agent_id": a.agentID,
	}
	points := make([]model.MetricPoint, 0, 4)

	// CPU usage (200ms sample average)
	cpuPercents, err := cpu.Percent(200*time.Millisecond, false)
	if err != nil {
		return nil, fmt.Errorf("CPU collect failed: %w", err)
	}
	if len(cpuPercents) > 0 {
		points = append(points, model.MetricPoint{
			Name:      "cpu_usage_percent",
			Labels:    labels,
			Value:     cpuPercents[0],
			Timestamp: now,
		})
	}

	// Memory
	vmStat, err := mem.VirtualMemory()
	if err != nil {
		return nil, fmt.Errorf("memory collect failed: %w", err)
	}
	points = append(points,
		model.MetricPoint{Name: "memory_usage_percent", Labels: labels, Value: vmStat.UsedPercent, Timestamp: now},
		model.MetricPoint{Name: "memory_used_bytes", Labels: labels, Value: float64(vmStat.Used), Timestamp: now},
	)

	// Disk
	diskPath := "/"
	if isWindows() {
		diskPath = "C:\\"
	}
	diskStat, err := disk.Usage(diskPath)
	if err != nil {
		diskStat = &disk.UsageStat{}
	}
	points = append(points, model.MetricPoint{
		Name:      "disk_usage_percent",
		Labels:    labels,
		Value:     diskStat.UsedPercent,
		Timestamp: now,
	})

	return points, nil
}

// collectAndSend gathers metrics and sends them; logs errors but does not exit.
func (a *Agent) collectAndSend() {
	points, err := a.Collect()
	if err != nil {
		fmt.Printf("[agent] collect failed: %v\n", err)
		return
	}
	if err := a.send(points); err != nil {
		fmt.Printf("[agent] send failed: %v\n", err)
	}
}

// send serializes points to JSON and POSTs to the ingest service.
func (a *Agent) send(points []model.MetricPoint) error {
	body, err := json.Marshal(points)
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}
	resp, err := a.client.Post(a.ingestURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("ingest server returned error: %d", resp.StatusCode)
	}
	return nil
}

// register sends a heartbeat to the agent registry. Silently ignored if registryURL is empty.
func (a *Agent) register() {
	if a.registryURL == "" {
		return
	}

	payload := map[string]interface{}{
		"agent_id":   a.agentID,
		"hostname":   a.hostname,
		"ip_address": a.ipAddress,
		"os":         a.osName,
		"arch":       a.arch,
		"labels":     map[string]string{"host": a.hostname},
	}
	body, _ := json.Marshal(payload)
	resp, err := a.client.Post(a.registryURL, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Printf("[agent] registry heartbeat failed: %v\n", err)
		return
	}
	resp.Body.Close()
}

// isWindows detects Windows via environment variable.
func isWindows() bool {
	return os.Getenv("SystemRoot") != "" || os.Getenv("WINDIR") != ""
}

// newUUID generates a random UUID v4 string.
func newUUID() string {
	var b [16]byte
	_, err := rand.Read(b[:])
	if err != nil {
		// Fallback: use time-based value (should not happen).
		return fmt.Sprintf("agent-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// localIP returns the preferred outbound IP of this machine.
func localIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "unknown"
	}
	defer conn.Close()
	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String()
}
