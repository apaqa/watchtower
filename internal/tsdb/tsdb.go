// Package tsdb provides the in-memory time-series database used by WatchTower.
package tsdb

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

// TSDB stores metric series in memory and optionally persists them to disk.
type TSDB struct {
	mu             sync.RWMutex
	series         map[string]*Series
	stopCh         chan struct{}
	storage        *StorageManager
	observersMu    sync.RWMutex
	observers      map[int]func([]model.MetricPoint)
	nextObserverID int
}

// New creates a TSDB without disk persistence.
func New() *TSDB {
	db := &TSDB{
		series:    make(map[string]*Series),
		stopCh:    make(chan struct{}),
		observers: make(map[int]func([]model.MetricPoint)),
	}
	go db.cleanupLoop()
	return db
}

// NewWithStorage creates a TSDB that persists chunks under dataDir.
func NewWithStorage(dataDir string) (*TSDB, error) {
	sm, err := NewStorageManager(dataDir)
	if err != nil {
		return nil, fmt.Errorf("create storage manager: %w", err)
	}

	db := &TSDB{
		series:    make(map[string]*Series),
		stopCh:    make(chan struct{}),
		storage:   sm,
		observers: make(map[int]func([]model.MetricPoint)),
	}
	go db.cleanupLoop()

	points, err := sm.LoadAll()
	if err != nil {
		return nil, fmt.Errorf("load persisted points: %w", err)
	}
	if len(points) > 0 {
		db.Write(points)
	}

	sm.StartFlushLoop()
	return db, nil
}

// Write appends metric points into the TSDB and notifies observers.
func (db *TSDB) Write(points []model.MetricPoint) {
	for _, mp := range points {
		fp := model.Fingerprint(mp.Name, mp.Labels)

		db.mu.RLock()
		s, ok := db.series[fp]
		db.mu.RUnlock()

		if !ok {
			db.mu.Lock()
			s, ok = db.series[fp]
			if !ok {
				s = newSeries(mp.Name, mp.Labels)
				db.series[fp] = s
			}
			db.mu.Unlock()
		}

		ts := mp.Timestamp
		if ts == 0 {
			ts = time.Now().UnixMilli()
		}
		dp := model.DataPoint{Timestamp: ts, Value: mp.Value}
		s.Append(dp)

		if db.storage != nil {
			db.storage.write(fp, mp.Name, mp.Labels, dp)
		}
	}

	db.notifyObservers(points)
}

// QueryRange returns points within [start, end].
func (db *TSDB) QueryRange(name string, labels map[string]string, start, end int64) []model.DataPoint {
	fp := model.Fingerprint(name, labels)

	db.mu.RLock()
	s, ok := db.series[fp]
	db.mu.RUnlock()

	if !ok {
		return nil
	}
	return s.QueryRange(start, end)
}

// QueryLatest returns up to n latest points for a series.
func (db *TSDB) QueryLatest(name string, labels map[string]string, n int) []model.DataPoint {
	fp := model.Fingerprint(name, labels)

	db.mu.RLock()
	s, ok := db.series[fp]
	db.mu.RUnlock()

	if !ok {
		return nil
	}
	return s.Latest(n)
}

// ListMetrics returns all unique metric names.
func (db *TSDB) ListMetrics() []string {
	db.mu.RLock()
	defer db.mu.RUnlock()

	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, s := range db.series {
		if _, exists := seen[s.Name]; exists {
			continue
		}
		seen[s.Name] = struct{}{}
		result = append(result, s.Name)
	}
	return result
}

// GetSeries returns all series for the given metric name.
func (db *TSDB) GetSeries(name string) []*Series {
	db.mu.RLock()
	defer db.mu.RUnlock()

	result := make([]*Series, 0)
	for _, s := range db.series {
		if s.Name == name {
			result = append(result, s)
		}
	}
	return result
}

// Stop stops background loops and flushes storage.
func (db *TSDB) Stop() {
	close(db.stopCh)
	if db.storage != nil {
		db.storage.Stop()
	}
}

func (db *TSDB) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			db.cleanup()
		case <-db.stopCh:
			return
		}
	}
}

func (db *TSDB) cleanup() {
	cutoff := cutoffTime()

	db.mu.RLock()
	series := make([]*Series, 0, len(db.series))
	for _, s := range db.series {
		series = append(series, s)
	}
	db.mu.RUnlock()

	for _, s := range series {
		if strings.HasSuffix(s.Name, ":1m") || strings.HasSuffix(s.Name, ":5m") {
			continue
		}
		s.Cleanup(cutoff)
	}
}

// GC triggers cleanup immediately.
func (db *TSDB) GC() {
	db.cleanup()
}

// TotalPoints returns the total point count across all series.
func (db *TSDB) TotalPoints() int {
	db.mu.RLock()
	defer db.mu.RUnlock()

	total := 0
	for _, s := range db.series {
		total += s.Len()
	}
	return total
}

// DeleteSeries removes all series that match the metric name.
func (db *TSDB) DeleteSeries(name string) int {
	db.mu.Lock()
	defer db.mu.Unlock()

	count := 0
	for fp, s := range db.series {
		if s.Name == name {
			delete(db.series, fp)
			count++
		}
	}
	return count
}

// Snapshot flushes storage to disk when persistence is enabled.
func (db *TSDB) Snapshot() bool {
	if db.storage == nil {
		return false
	}
	db.storage.Flush()
	return true
}

// SeriesCount returns the current number of series.
func (db *TSDB) SeriesCount() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return len(db.series)
}

// AddWriteObserver registers a callback for every Write call.
func (db *TSDB) AddWriteObserver(fn func([]model.MetricPoint)) int {
	if fn == nil {
		return 0
	}
	db.observersMu.Lock()
	defer db.observersMu.Unlock()

	db.nextObserverID++
	id := db.nextObserverID
	db.observers[id] = fn
	return id
}

// RemoveWriteObserver unregisters a previously registered callback.
func (db *TSDB) RemoveWriteObserver(id int) {
	if id == 0 {
		return
	}
	db.observersMu.Lock()
	delete(db.observers, id)
	db.observersMu.Unlock()
}

func (db *TSDB) notifyObservers(points []model.MetricPoint) {
	db.observersMu.RLock()
	if len(db.observers) == 0 {
		db.observersMu.RUnlock()
		return
	}
	observers := make([]func([]model.MetricPoint), 0, len(db.observers))
	for _, fn := range db.observers {
		observers = append(observers, fn)
	}
	db.observersMu.RUnlock()

	copied := cloneMetricPoints(points)
	for _, fn := range observers {
		fn(copied)
	}
}

func cloneMetricPoints(points []model.MetricPoint) []model.MetricPoint {
	if len(points) == 0 {
		return nil
	}
	out := make([]model.MetricPoint, len(points))
	for i, pt := range points {
		out[i] = pt
		if pt.Labels != nil {
			labels := make(map[string]string, len(pt.Labels))
			for k, v := range pt.Labels {
				labels[k] = v
			}
			out[i].Labels = labels
		}
	}
	return out
}
