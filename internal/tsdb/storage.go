// Package tsdb - 持久化存储层
// 将时间序列数据以压缩块（Chunk）形式写入磁盘，采用 Gorilla 论文中的
// Delta-of-Delta 时间戳编码和 XOR 浮点值编码，实现高压缩率。
package tsdb

import (
	"fmt"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/apaqa/watchtower/internal/model"
)

const (
	// 块文件魔数，用于格式识别
	chunkMagic = "WTCK"
	// 块文件格式版本号
	chunkVersion = uint8(1)
	// 每个块最长存储 1 小时的数据
	chunkMaxDuration = time.Hour
	// 活跃块后台刷盘间隔
	flushInterval = 30 * time.Second
)

// ─────────────────────────────────────────────────────────────────────────────
// 位流读写器（Gorilla 压缩基础设施）
// ─────────────────────────────────────────────────────────────────────────────

// bitWriter 将位序列按大端序写入字节缓冲区
type bitWriter struct {
	data []byte
	buf  byte // 当前未满字节的缓冲
	bits int  // buf 中已写入的位数（0‑8）
}

// writeBit 写入单个位（true=1, false=0）
func (bw *bitWriter) writeBit(v bool) {
	bw.buf <<= 1
	if v {
		bw.buf |= 1
	}
	bw.bits++
	if bw.bits == 8 {
		bw.data = append(bw.data, bw.buf)
		bw.buf = 0
		bw.bits = 0
	}
}

// writeBits 从高位到低位写入 n 个位
func (bw *bitWriter) writeBits(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		bw.writeBit((v>>uint(i))&1 == 1)
	}
}

// flush 将剩余不足 8 位的数据低位补零后写入，返回完整字节切片
func (bw *bitWriter) flush() []byte {
	if bw.bits > 0 {
		bw.buf <<= uint(8 - bw.bits)
		bw.data = append(bw.data, bw.buf)
		bw.buf = 0
		bw.bits = 0
	}
	return bw.data
}

// bitReader 从字节切片中逐位（大端序）读取
type bitReader struct {
	data []byte
	pos  int  // 当前字节下标
	cur  byte // 当前字节
	bits int  // cur 中剩余未读位数
}

func newBitReader(data []byte) *bitReader {
	return &bitReader{data: data}
}

// readBit 读取单个位；数据耗尽时返回错误
func (br *bitReader) readBit() (bool, error) {
	if br.bits == 0 {
		if br.pos >= len(br.data) {
			return false, fmt.Errorf("bitReader: 数据流已耗尽")
		}
		br.cur = br.data[br.pos]
		br.pos++
		br.bits = 8
	}
	bit := (br.cur & 0x80) != 0
	br.cur <<= 1
	br.bits--
	return bit, nil
}

// readBits 读取 n 个位并返回 uint64（高位先读）
func (br *bitReader) readBits(n int) (uint64, error) {
	var v uint64
	for i := 0; i < n; i++ {
		bit, err := br.readBit()
		if err != nil {
			return 0, err
		}
		v = (v << 1)
		if bit {
			v |= 1
		}
	}
	return v, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Gorilla 时间戳 Delta-of-Delta 编码
// ─────────────────────────────────────────────────────────────────────────────

// encodeTimestamps 将时间戳序列使用 delta-of-delta 编码写入位流
//
// 编码规则（参照 Gorilla 论文）：
//   第 0 个时间戳：原始 64 位
//   第 1 个 delta（t1−t0）：原始 32 位
//   后续 delta-of-delta（DoD）：
//     '0'          → DoD == 0
//     '10' + 7 bits → −64 ≤ DoD ≤ 63  (zigzag)
//     '110' + 9 bits → −256 ≤ DoD ≤ 255
//     '1110' + 12 bits → −2048 ≤ DoD ≤ 2047
//     '1111' + 64 bits → 任意值
func encodeTimestamps(bw *bitWriter, timestamps []int64) {
	if len(timestamps) == 0 {
		return
	}
	// 第一个时间戳：原始 64 位
	bw.writeBits(uint64(timestamps[0]), 64)
	if len(timestamps) == 1 {
		return
	}
	// 第一个 delta：原始 32 位（毫秒级别足够）
	firstDelta := timestamps[1] - timestamps[0]
	bw.writeBits(uint64(uint32(firstDelta)), 32)

	prevDelta := firstDelta
	for i := 2; i < len(timestamps); i++ {
		delta := timestamps[i] - timestamps[i-1]
		dod := delta - prevDelta
		prevDelta = delta
		encodeDoD(bw, dod)
	}
}

// encodeDoD 将单个 delta-of-delta 值编码写入位流
func encodeDoD(bw *bitWriter, dod int64) {
	switch {
	case dod == 0:
		// 控制位 '0'
		bw.writeBit(false)
	case dod >= -64 && dod <= 63:
		// 控制位 '10'，7 位 zigzag
		bw.writeBit(true)
		bw.writeBit(false)
		bw.writeBits(zigzagEncode(dod), 7)
	case dod >= -256 && dod <= 255:
		// 控制位 '110'，9 位 zigzag
		bw.writeBit(true)
		bw.writeBit(true)
		bw.writeBit(false)
		bw.writeBits(zigzagEncode(dod), 9)
	case dod >= -2048 && dod <= 2047:
		// 控制位 '1110'，12 位 zigzag
		bw.writeBit(true)
		bw.writeBit(true)
		bw.writeBit(true)
		bw.writeBit(false)
		bw.writeBits(zigzagEncode(dod), 12)
	default:
		// 控制位 '1111'，完整 64 位
		bw.writeBit(true)
		bw.writeBit(true)
		bw.writeBit(true)
		bw.writeBit(true)
		bw.writeBits(uint64(dod), 64)
	}
}

// decodeTimestamps 从位流解码时间戳序列
func decodeTimestamps(br *bitReader, count int) ([]int64, error) {
	if count == 0 {
		return nil, nil
	}
	timestamps := make([]int64, count)

	v, err := br.readBits(64)
	if err != nil {
		return nil, err
	}
	timestamps[0] = int64(v)
	if count == 1 {
		return timestamps, nil
	}

	// 第一个 delta：32 位（有符号扩展）
	v, err = br.readBits(32)
	if err != nil {
		return nil, err
	}
	firstDelta := int64(int32(v))
	timestamps[1] = timestamps[0] + firstDelta

	prevDelta := firstDelta
	for i := 2; i < count; i++ {
		dod, err := decodeDoD(br)
		if err != nil {
			return nil, err
		}
		delta := prevDelta + dod
		timestamps[i] = timestamps[i-1] + delta
		prevDelta = delta
	}
	return timestamps, nil
}

// decodeDoD 从位流解码单个 delta-of-delta 值
func decodeDoD(br *bitReader) (int64, error) {
	b0, err := br.readBit()
	if err != nil {
		return 0, err
	}
	if !b0 {
		return 0, nil // dod == 0
	}
	b1, err := br.readBit()
	if err != nil {
		return 0, err
	}
	if !b1 {
		// '10'：7 位 zigzag
		v, err := br.readBits(7)
		if err != nil {
			return 0, err
		}
		return zigzagDecode(v), nil
	}
	b2, err := br.readBit()
	if err != nil {
		return 0, err
	}
	if !b2 {
		// '110'：9 位 zigzag
		v, err := br.readBits(9)
		if err != nil {
			return 0, err
		}
		return zigzagDecode(v), nil
	}
	b3, err := br.readBit()
	if err != nil {
		return 0, err
	}
	if !b3 {
		// '1110'：12 位 zigzag
		v, err := br.readBits(12)
		if err != nil {
			return 0, err
		}
		return zigzagDecode(v), nil
	}
	// '1111'：完整 64 位
	v, err := br.readBits(64)
	if err != nil {
		return 0, err
	}
	return int64(v), nil
}

// zigzagEncode 将有符号 int64 转换为无符号 zigzag 编码
// 映射规则：0→0, -1→1, 1→2, -2→3, 2→4, …
func zigzagEncode(v int64) uint64 {
	return uint64((v << 1) ^ (v >> 63))
}

// zigzagDecode 将 zigzag 编码还原为有符号 int64
func zigzagDecode(v uint64) int64 {
	return int64((v >> 1) ^ -(v & 1))
}

// ─────────────────────────────────────────────────────────────────────────────
// Gorilla 浮点值 XOR 编码
// ─────────────────────────────────────────────────────────────────────────────

// encodeValues 将 float64 序列使用 XOR 编码写入位流
//
// 编码规则（参照 Gorilla 论文）：
//   第 0 个值：原始 64 位 IEEE-754
//   后续值：与前值 XOR
//     '0'          → XOR == 0（值相同）
//     '10' + meaningful bits → 前导/尾随零数量与上次相同，只写有效位
//     '11' + 5位leading + 6位length + meaningful bits → 新前导/尾随零
func encodeValues(bw *bitWriter, values []float64) {
	if len(values) == 0 {
		return
	}
	// 第一个值：原始 64 位
	bw.writeBits(math.Float64bits(values[0]), 64)

	prevBits := math.Float64bits(values[0])
	var prevLeading, prevTrailing, prevMeaningful uint8
	hasPrev := false

	for i := 1; i < len(values); i++ {
		curBits := math.Float64bits(values[i])
		xor := curBits ^ prevBits
		prevBits = curBits

		if xor == 0 {
			// 值与上一个相同
			bw.writeBit(false)
			continue
		}
		bw.writeBit(true)

		// 计算前导/尾随零数量（前导零上限 31，以适应 5-bit 编码）
		leading := uint8(bits.LeadingZeros64(xor))
		if leading > 31 {
			leading = 31
		}
		trailing := uint8(bits.TrailingZeros64(xor))
		meaningful := 64 - leading - trailing

		// 判断是否可复用上次的有效位窗口
		if hasPrev && leading >= prevLeading && trailing >= prevTrailing {
			bw.writeBit(false) // 复用窗口
			bw.writeBits(xor>>prevTrailing, int(prevMeaningful))
		} else {
			bw.writeBit(true) // 新窗口
			bw.writeBits(uint64(leading), 5)
			bw.writeBits(uint64(meaningful-1), 6) // 减 1 使 6 位可表示 1‑64
			bw.writeBits(xor>>trailing, int(meaningful))
			prevLeading = leading
			prevTrailing = trailing
			prevMeaningful = meaningful
			hasPrev = true
		}
	}
}

// decodeValues 从位流解码 float64 序列
func decodeValues(br *bitReader, count int) ([]float64, error) {
	if count == 0 {
		return nil, nil
	}
	values := make([]float64, count)

	v, err := br.readBits(64)
	if err != nil {
		return nil, err
	}
	values[0] = math.Float64frombits(v)
	prevBits := v
	var prevTrailing, prevMeaningful uint8

	for i := 1; i < count; i++ {
		b, err := br.readBit()
		if err != nil {
			return nil, err
		}
		if !b {
			// XOR == 0，值与前一个相同
			values[i] = math.Float64frombits(prevBits)
			continue
		}

		hasNew, err := br.readBit()
		if err != nil {
			return nil, err
		}

		var xor uint64
		if !hasNew {
			// 复用上次窗口
			xorMeaningful, err := br.readBits(int(prevMeaningful))
			if err != nil {
				return nil, err
			}
			xor = xorMeaningful << prevTrailing
		} else {
			// 读取新窗口
			leading, err := br.readBits(5)
			if err != nil {
				return nil, err
			}
			meaningfulMinus1, err := br.readBits(6)
			if err != nil {
				return nil, err
			}
			meaningful := uint8(meaningfulMinus1) + 1
			trailing := 64 - uint8(leading) - meaningful

			xorMeaningful, err := br.readBits(int(meaningful))
			if err != nil {
				return nil, err
			}
			xor = xorMeaningful << trailing
			prevTrailing = trailing
			prevMeaningful = meaningful
		}

		curBits := prevBits ^ xor
		values[i] = math.Float64frombits(curBits)
		prevBits = curBits
	}
	return values, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 块（Chunk）序列化 / 反序列化
// ─────────────────────────────────────────────────────────────────────────────

// chunk 代表某条序列在一段连续时间内的数据
type chunk struct {
	fp     string
	name   string
	labels map[string]string
	points []model.DataPoint
}

// encodeChunk 将块序列化为二进制字节
//
// 文件格式：
//   MAGIC(4) + VERSION(1) + NAME_LEN(2,BE) + NAME +
//   LABEL_COUNT(2,BE) + [KEY_LEN(2)+KEY+VAL_LEN(2)+VAL]... +
//   POINT_COUNT(4,BE) +
//   TS_DATA_LEN(4,BE) + TS_DATA(bit-packed) +
//   VAL_DATA_LEN(4,BE) + VAL_DATA(bit-packed)
func encodeChunk(c *chunk) ([]byte, error) {
	count := len(c.points)
	if count == 0 {
		return nil, fmt.Errorf("块为空，无需编码")
	}

	// 分离时间戳和值
	timestamps := make([]int64, count)
	values := make([]float64, count)
	for i, dp := range c.points {
		timestamps[i] = dp.Timestamp
		values[i] = dp.Value
	}

	// 压缩时间戳
	tsBw := &bitWriter{}
	encodeTimestamps(tsBw, timestamps)
	tsData := tsBw.flush()

	// 压缩浮点值
	valBw := &bitWriter{}
	encodeValues(valBw, values)
	valData := valBw.flush()

	// 构建标签列表（顺序稳定）
	type kv struct{ k, v string }
	nameBytes := []byte(c.name)

	// 收集标签键值对
	kvList := make([]kv, 0, len(c.labels))
	for k, v := range c.labels {
		kvList = append(kvList, kv{k, v})
	}

	// 拼接二进制头部
	var buf []byte
	buf = append(buf, []byte(chunkMagic)...)
	buf = append(buf, chunkVersion)
	// 指标名称
	buf = appendUint16BE(buf, uint16(len(nameBytes)))
	buf = append(buf, nameBytes...)
	// 标签
	buf = appendUint16BE(buf, uint16(len(kvList)))
	for _, pair := range kvList {
		kb := []byte(pair.k)
		vb := []byte(pair.v)
		buf = appendUint16BE(buf, uint16(len(kb)))
		buf = append(buf, kb...)
		buf = appendUint16BE(buf, uint16(len(vb)))
		buf = append(buf, vb...)
	}
	// 数据点数量
	buf = appendUint32BE(buf, uint32(count))
	// 时间戳压缩数据
	buf = appendUint32BE(buf, uint32(len(tsData)))
	buf = append(buf, tsData...)
	// 值压缩数据
	buf = appendUint32BE(buf, uint32(len(valData)))
	buf = append(buf, valData...)

	return buf, nil
}

// decodeChunk 从字节切片反序列化块
func decodeChunk(data []byte) (*chunk, error) {
	if len(data) < 5 {
		return nil, fmt.Errorf("块数据太短（%d 字节）", len(data))
	}
	pos := 0

	// 魔数校验
	if string(data[pos:pos+4]) != chunkMagic {
		return nil, fmt.Errorf("块魔数不匹配")
	}
	pos += 4

	// 版本校验
	if data[pos] != chunkVersion {
		return nil, fmt.Errorf("不支持的块版本 %d", data[pos])
	}
	pos++

	readUint16 := func() (uint16, error) {
		if pos+2 > len(data) {
			return 0, fmt.Errorf("数据截断（uint16，pos=%d）", pos)
		}
		v := uint16(data[pos])<<8 | uint16(data[pos+1])
		pos += 2
		return v, nil
	}
	readUint32 := func() (uint32, error) {
		if pos+4 > len(data) {
			return 0, fmt.Errorf("数据截断（uint32，pos=%d）", pos)
		}
		v := uint32(data[pos])<<24 | uint32(data[pos+1])<<16 |
			uint32(data[pos+2])<<8 | uint32(data[pos+3])
		pos += 4
		return v, nil
	}
	readBytes := func(n int) ([]byte, error) {
		if pos+n > len(data) {
			return nil, fmt.Errorf("数据截断（%d 字节，pos=%d）", n, pos)
		}
		b := data[pos : pos+n]
		pos += n
		return b, nil
	}

	// 指标名称
	nameLen, err := readUint16()
	if err != nil {
		return nil, err
	}
	nameBytes, err := readBytes(int(nameLen))
	if err != nil {
		return nil, err
	}
	name := string(nameBytes)

	// 标签
	labelCount, err := readUint16()
	if err != nil {
		return nil, err
	}
	labels := make(map[string]string, labelCount)
	for i := 0; i < int(labelCount); i++ {
		kLen, err := readUint16()
		if err != nil {
			return nil, err
		}
		kb, err := readBytes(int(kLen))
		if err != nil {
			return nil, err
		}
		vLen, err := readUint16()
		if err != nil {
			return nil, err
		}
		vb, err := readBytes(int(vLen))
		if err != nil {
			return nil, err
		}
		labels[string(kb)] = string(vb)
	}

	// 数据点数量
	count, err := readUint32()
	if err != nil {
		return nil, err
	}

	// 时间戳数据
	tsDataLen, err := readUint32()
	if err != nil {
		return nil, err
	}
	tsData, err := readBytes(int(tsDataLen))
	if err != nil {
		return nil, err
	}

	// 值数据
	valDataLen, err := readUint32()
	if err != nil {
		return nil, err
	}
	valData, err := readBytes(int(valDataLen))
	if err != nil {
		return nil, err
	}

	// 解码
	tsBr := newBitReader(tsData)
	timestamps, err := decodeTimestamps(tsBr, int(count))
	if err != nil {
		return nil, fmt.Errorf("时间戳解码失败: %w", err)
	}
	valBr := newBitReader(valData)
	values, err := decodeValues(valBr, int(count))
	if err != nil {
		return nil, fmt.Errorf("值解码失败: %w", err)
	}

	points := make([]model.DataPoint, count)
	for i := range points {
		points[i] = model.DataPoint{Timestamp: timestamps[i], Value: values[i]}
	}

	fp := model.Fingerprint(name, labels)
	return &chunk{fp: fp, name: name, labels: labels, points: points}, nil
}

// 大端序辅助函数
func appendUint16BE(buf []byte, v uint16) []byte {
	return append(buf, byte(v>>8), byte(v))
}

func appendUint32BE(buf []byte, v uint32) []byte {
	return append(buf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// ─────────────────────────────────────────────────────────────────────────────
// StorageManager：管理活跃块与磁盘 I/O
// ─────────────────────────────────────────────────────────────────────────────

// activeChunk 是正在积累数据的内存块
type activeChunk struct {
	mu        sync.Mutex
	fp        string
	name      string
	labels    map[string]string
	points    []model.DataPoint
	startTime int64 // 块起始时间戳（Unix 毫秒）
}

// StorageManager 负责块的持久化：写入、刷盘、加载和轮换
type StorageManager struct {
	dataDir string
	mu      sync.Mutex
	active  map[string]*activeChunk // 指纹 → 活跃块
	stopCh  chan struct{}
}

// NewStorageManager 创建 StorageManager 并确保数据目录存在
func NewStorageManager(dataDir string) (*StorageManager, error) {
	chunksDir := filepath.Join(dataDir, "chunks")
	if err := os.MkdirAll(chunksDir, 0755); err != nil {
		return nil, fmt.Errorf("创建块目录失败: %w", err)
	}
	return &StorageManager{
		dataDir: dataDir,
		active:  make(map[string]*activeChunk),
		stopCh:  make(chan struct{}),
	}, nil
}

// write 将一个数据点添加到对应序列的活跃块；若块时间跨度超过 1 小时则先轮换
func (sm *StorageManager) write(fp, name string, labels map[string]string, dp model.DataPoint) {
	sm.mu.Lock()
	ac, ok := sm.active[fp]
	if !ok {
		lblsCopy := make(map[string]string, len(labels))
		for k, v := range labels {
			lblsCopy[k] = v
		}
		ac = &activeChunk{
			fp:        fp,
			name:      name,
			labels:    lblsCopy,
			startTime: dp.Timestamp,
		}
		sm.active[fp] = ac
	}
	sm.mu.Unlock()

	ac.mu.Lock()
	// 检查块是否已满（超过 1 小时），若是则先刷盘并轮换
	if len(ac.points) > 0 &&
		dp.Timestamp-ac.startTime > int64(chunkMaxDuration/time.Millisecond) {
		old := &chunk{
			fp:     ac.fp,
			name:   ac.name,
			labels: ac.labels,
			points: ac.points,
		}
		ac.points = nil
		ac.startTime = dp.Timestamp
		ac.mu.Unlock()
		_ = sm.writeChunkToDisk(old) // 轮换时同步刷盘
		ac.mu.Lock()
	}
	ac.points = append(ac.points, dp)
	ac.mu.Unlock()
}

// Flush 将所有活跃块的当前数据持久化到磁盘（活跃块继续接收新数据）
func (sm *StorageManager) Flush() {
	sm.mu.Lock()
	activeList := make([]*activeChunk, 0, len(sm.active))
	for _, ac := range sm.active {
		activeList = append(activeList, ac)
	}
	sm.mu.Unlock()

	for _, ac := range activeList {
		ac.mu.Lock()
		if len(ac.points) == 0 {
			ac.mu.Unlock()
			continue
		}
		// 复制一份数据后再刷盘，避免长时间持锁
		c := &chunk{
			fp:     ac.fp,
			name:   ac.name,
			labels: ac.labels,
			points: append([]model.DataPoint(nil), ac.points...),
		}
		ac.mu.Unlock()
		_ = sm.writeChunkToDisk(c)
	}
}

// LoadAll 从磁盘加载所有块，返回所有 MetricPoint（用于启动时恢复内存状态）
func (sm *StorageManager) LoadAll() ([]model.MetricPoint, error) {
	chunksDir := filepath.Join(sm.dataDir, "chunks")
	entries, err := os.ReadDir(chunksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取块目录失败: %w", err)
	}

	var allPoints []model.MetricPoint
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".wtkc") {
			continue
		}
		path := filepath.Join(chunksDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue // 跳过无法读取的文件
		}
		c, err := decodeChunk(data)
		if err != nil {
			continue // 跳过损坏的块
		}
		for _, dp := range c.points {
			allPoints = append(allPoints, model.MetricPoint{
				Name:      c.name,
				Labels:    c.labels,
				Value:     dp.Value,
				Timestamp: dp.Timestamp,
			})
		}
	}
	return allPoints, nil
}

// Stop 停止后台刷盘协程，并执行最终一次刷盘
func (sm *StorageManager) Stop() {
	close(sm.stopCh)
	sm.Flush() // 退出时确保数据落盘
}

// StartFlushLoop 启动后台刷盘协程（每 30 秒执行一次）
func (sm *StorageManager) StartFlushLoop() {
	go func() {
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sm.Flush()
			case <-sm.stopCh:
				return
			}
		}
	}()
}

// writeChunkToDisk 将块编码后写入磁盘文件
// 文件名格式：{fingerprint}_{start_unix_ms}.wtkc
func (sm *StorageManager) writeChunkToDisk(c *chunk) error {
	if len(c.points) == 0 {
		return nil
	}
	data, err := encodeChunk(c)
	if err != nil {
		return fmt.Errorf("块编码失败: %w", err)
	}
	startMs := c.points[0].Timestamp
	filename := fmt.Sprintf("%s_%d.wtkc", c.fp, startMs)
	path := filepath.Join(sm.dataDir, "chunks", filename)
	return os.WriteFile(path, data, 0644)
}
