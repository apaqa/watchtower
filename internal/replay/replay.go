// Package replay records metric streams to files and replays them into the TSDB.
package replay

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
	"github.com/apaqa/watchtower/internal/tsdb"
)

// Manager owns recording and replay operations.
type Manager struct {
	db            *tsdb.TSDB
	recordingsDir string
	sleepFn       func(time.Duration)
	nowFn         func() time.Time

	mu         sync.Mutex
	recording  bool
	recordFile *os.File
	recordPath string
	observerID int
	playbackMu sync.Mutex
}

// RecordingInfo describes a recording file.
type RecordingInfo struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	SizeBytes    int64  `json:"size_bytes"`
	ModifiedAtMs int64  `json:"modified_at_ms"`
}

// PlayResult is the replay execution result.
type PlayResult struct {
	File          string  `json:"file"`
	Speed         string  `json:"speed"`
	Multiplier    float64 `json:"multiplier"`
	ReplayedCount int     `json:"replayed_count"`
}

// New creates a replay manager under the given data directory.
func New(db *tsdb.TSDB, dataDir string) (*Manager, error) {
	recordingsDir := filepath.Join(dataDir, "recordings")
	if err := os.MkdirAll(recordingsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create recordings dir: %w", err)
	}
	return &Manager{
		db:            db,
		recordingsDir: recordingsDir,
		sleepFn:       time.Sleep,
		nowFn:         time.Now,
	}, nil
}

// StartRecording begins recording all metric writes into a new .rec file.
func (m *Manager) StartRecording() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.recording {
		return "", fmt.Errorf("recording already running")
	}

	name := m.nowFn().Format("20060102-150405") + ".rec"
	path := filepath.Join(m.recordingsDir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("create recording file: %w", err)
	}

	writerMu := sync.Mutex{}
	observerID := m.db.AddWriteObserver(func(points []model.MetricPoint) {
		if len(points) == 0 {
			return
		}
		writerMu.Lock()
		defer writerMu.Unlock()
		for _, pt := range points {
			payload, err := json.Marshal(pt)
			if err != nil {
				continue
			}
			_, _ = file.Write(append(payload, '\n'))
		}
		_ = file.Sync()
	})

	m.recording = true
	m.recordFile = file
	m.recordPath = path
	m.observerID = observerID
	return path, nil
}

// StopRecording stops the current recording and closes the file.
func (m *Manager) StopRecording() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.recording {
		return "", fmt.Errorf("recording not running")
	}

	m.db.RemoveWriteObserver(m.observerID)
	path := m.recordPath
	err := m.recordFile.Close()
	m.recording = false
	m.recordFile = nil
	m.recordPath = ""
	m.observerID = 0
	if err != nil {
		return "", fmt.Errorf("close recording file: %w", err)
	}
	return path, nil
}

// ListRecordings lists available recordings newest first.
func (m *Manager) ListRecordings() ([]RecordingInfo, error) {
	entries, err := os.ReadDir(m.recordingsDir)
	if err != nil {
		return nil, fmt.Errorf("read recordings dir: %w", err)
	}

	list := make([]RecordingInfo, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".rec") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		list = append(list, RecordingInfo{
			Name:         entry.Name(),
			Path:         filepath.Join(m.recordingsDir, entry.Name()),
			SizeBytes:    info.Size(),
			ModifiedAtMs: info.ModTime().UnixMilli(),
		})
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].ModifiedAtMs > list[j].ModifiedAtMs
	})
	return list, nil
}

// Play replays a recording file back into the TSDB at the given speed.
func (m *Manager) Play(fileName, speed string) (PlayResult, error) {
	m.playbackMu.Lock()
	defer m.playbackMu.Unlock()

	multiplier, err := parseSpeed(speed)
	if err != nil {
		return PlayResult{}, err
	}

	path, err := m.resolveRecording(fileName)
	if err != nil {
		return PlayResult{}, err
	}

	file, err := os.Open(path)
	if err != nil {
		return PlayResult{}, fmt.Errorf("open recording: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var (
		prevTs int64
		count  int
	)
	for scanner.Scan() {
		var pt model.MetricPoint
		if err := json.Unmarshal(scanner.Bytes(), &pt); err != nil {
			return PlayResult{}, fmt.Errorf("decode recording line: %w", err)
		}
		if prevTs > 0 && pt.Timestamp > prevTs {
			delay := time.Duration(float64(pt.Timestamp-prevTs)/multiplier) * time.Millisecond
			if delay > 0 {
				m.sleepFn(delay)
			}
		}
		m.db.Write([]model.MetricPoint{pt})
		prevTs = pt.Timestamp
		count++
	}
	if err := scanner.Err(); err != nil {
		return PlayResult{}, fmt.Errorf("scan recording: %w", err)
	}

	if speed == "" {
		speed = "1x"
	}
	return PlayResult{
		File:          filepath.Base(path),
		Speed:         speed,
		Multiplier:    multiplier,
		ReplayedCount: count,
	}, nil
}

// RegisterRoutes registers replay APIs into the ingest server.
func RegisterRoutes(mux *http.ServeMux, m *Manager) {
	mux.HandleFunc("/api/v1/replay/record/start", func(w http.ResponseWriter, r *http.Request) {
		writeJSONHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
			return
		}
		path, err := m.StartRecording()
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"file": filepath.Base(path), "path": path})
	})

	mux.HandleFunc("/api/v1/replay/record/stop", func(w http.ResponseWriter, r *http.Request) {
		writeJSONHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
			return
		}
		path, err := m.StopRecording()
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"file": filepath.Base(path), "path": path})
	})

	mux.HandleFunc("/api/v1/replay/play", func(w http.ResponseWriter, r *http.Request) {
		writeJSONHeaders(w)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
			return
		}
		fileName := r.URL.Query().Get("file")
		if fileName == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "file is required"})
			return
		}
		result, err := m.Play(fileName, r.URL.Query().Get("speed"))
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "not found") {
				status = http.StatusNotFound
			}
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(result)
	})

	mux.HandleFunc("/api/v1/replay/recordings", func(w http.ResponseWriter, r *http.Request) {
		writeJSONHeaders(w)
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
			return
		}
		list, err := m.ListRecordings()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		if list == nil {
			list = []RecordingInfo{}
		}
		json.NewEncoder(w).Encode(list)
	})
}

func writeJSONHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
}

func (m *Manager) resolveRecording(fileName string) (string, error) {
	base := filepath.Base(fileName)
	if base != fileName || strings.TrimSpace(base) == "" {
		return "", fmt.Errorf("invalid recording file")
	}
	path := filepath.Join(m.recordingsDir, base)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("recording not found")
		}
		return "", fmt.Errorf("stat recording: %w", err)
	}
	return path, nil
}

func parseSpeed(value string) (float64, error) {
	if value == "" {
		return 1, nil
	}
	clean := strings.TrimSpace(strings.ToLower(value))
	clean = strings.TrimSuffix(clean, "x")
	multiplier, err := strconv.ParseFloat(clean, 64)
	if err != nil || multiplier <= 0 {
		return 0, fmt.Errorf("invalid speed multiplier")
	}
	return multiplier, nil
}
