package services

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"unsafe"
)

var clientIPGeoPublicCases = []struct {
	raw     string
	country string
}{
	{"8.8.8.8", "US"},
	{"8.8.8.0", "US"},
	{"8.8.8.255", "US"},
	{"114.114.114.114", "CN"},
	{"2001:4860:4860::8888", "US"},
	{"2001:4860:4860:ffff:ffff:ffff:ffff:ffff", "US"},
	{"2400:3200::1", "CN"},
	{"::ffff:8.8.8.8", "US"},
	{"::ffff:808:808", "US"},
	{"[2001:4860:4860::8888]", "US"},
	{"2001:4860:4860::8888%eth0", "US"},
	{"[2400:3200::1%eth0]", "CN"},
}

var clientIPGeoPrivateCases = []string{
	"127.0.0.1", "127.255.255.255",
	"10.0.0.1", "10.255.255.255",
	"172.16.0.1", "172.31.255.255",
	"192.168.1.1", "192.168.255.255",
	"100.64.0.1", "100.127.255.255",
	"169.254.1.1", "169.254.255.255",
	"224.0.0.1", "224.0.0.255", "0.0.0.0",
	"::1", "[::1]", "::",
	"fc00::1", "fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
	"fe80::1", "febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
	"fe80::1%eth0", "[fe80::1%eth0]", "ff02::1", "ff02::1%eth0",
	"::ffff:127.0.0.1", "::ffff:192.168.1.1", "::ffff:100.64.0.1", "::ffff:0.0.0.0",
}

func TestClientIPGeoColdStart(testContext *testing.T) {
	if os.Getenv("SBPM_IPGEO_COLD_TEST") != "1" {
		command := exec.Command(os.Args[0], "-test.run=^TestClientIPGeoColdStart$", "-test.count=1")
		command.Env = append(os.Environ(), "SBPM_IPGEO_COLD_TEST=1")
		if output, err := command.CombinedOutput(); err != nil {
			testContext.Fatalf("冷启动查询测试失败：%v\n%s", err, output)
		}
		return
	}
	for _, raw := range clientIPGeoPrivateCases {
		if actual := LookupClientIPGeo(raw); actual != (ClientIPGeo{IsPrivate: true}) {
			testContext.Errorf("内网地址 %q 返回 %+v", raw, actual)
		}
	}
	for _, raw := range []string{"", " ", "not-an-ip", "256.1.1.1", "8.8.8.8:53", "8.8.8.8%eth0", "[8.8.8.8]", "::gg", "fe80::1%", "[::1", "::1]", "[::1]:80", "[]"} {
		if actual := LookupClientIPGeo(raw); actual != (ClientIPGeo{}) {
			testContext.Errorf("非法地址 %q 返回 %+v", raw, actual)
		}
	}
	if ipgeoStore != nil {
		testContext.Fatal("内网或非法地址触发了地区表加载")
	}
	testContext.Run("ConcurrentInitialization", testClientIPGeoConcurrent)
	if ipgeoStore == nil || len(ipgeoStore.v4Blocks) == 0 || len(ipgeoStore.v6Blocks) == 0 {
		testContext.Fatal("公网查询未加载地区表")
	}
}

func TestClientIPGeoPublic(testContext *testing.T) {
	for _, testCase := range clientIPGeoPublicCases {
		testContext.Run(testCase.raw, func(testContext *testing.T) {
			if actual := LookupClientIPGeo(testCase.raw); actual != (ClientIPGeo{CountryCode: testCase.country}) {
				testContext.Fatalf("公网地址 %q 返回 %+v，期望 %s", testCase.raw, actual, testCase.country)
			}
		})
	}
}

func testClientIPGeoConcurrent(testContext *testing.T) {
	var workers sync.WaitGroup
	start := make(chan struct{})
	for worker := 0; worker < 32; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for iteration := 0; iteration < 32; iteration++ {
				for _, testCase := range clientIPGeoPublicCases {
					if actual := LookupClientIPGeo(testCase.raw); actual != (ClientIPGeo{CountryCode: testCase.country}) {
						testContext.Errorf("并发查询 %q 返回 %+v", testCase.raw, actual)
						return
					}
				}
				if actual := LookupClientIPGeo("fe80::1%eth0"); actual != (ClientIPGeo{IsPrivate: true}) {
					testContext.Errorf("并发内网查询返回 %+v", actual)
					return
				}
			}
		}()
	}
	close(start)
	workers.Wait()
}

func TestClientIPGeoConcurrent(testContext *testing.T) {
	testClientIPGeoConcurrent(testContext)
}

func TestClientIPGeoZeroAllocs(testContext *testing.T) {
	if actual := LookupClientIPGeo("8.8.8.8"); actual.CountryCode != "US" {
		testContext.Fatalf("预热公网查询返回 %+v", actual)
	}
	for _, testCase := range clientIPGeoPublicCases {
		testContext.Run(testCase.raw, func(testContext *testing.T) {
			var actual ClientIPGeo
			allocations := testing.AllocsPerRun(100, func() { actual = LookupClientIPGeo(testCase.raw) })
			if actual != (ClientIPGeo{CountryCode: testCase.country}) || allocations != 0 {
				testContext.Fatalf("公网查询 %q 返回 %+v，堆分配次数 %g", testCase.raw, actual, allocations)
			}
		})
	}
	for _, raw := range clientIPGeoPrivateCases {
		testContext.Run(raw, func(testContext *testing.T) {
			var actual ClientIPGeo
			allocations := testing.AllocsPerRun(100, func() { actual = LookupClientIPGeo(raw) })
			if actual != (ClientIPGeo{IsPrivate: true}) || allocations != 0 {
				testContext.Fatalf("内网查询 %q 返回 %+v，堆分配次数 %g", raw, actual, allocations)
			}
		})
	}
}

func TestClientIPGeoSyntheticSBG2(testContext *testing.T) {
	store := newIPGeoStore(buildTestIPGeoData(
		[]string{"CN", "US"},
		[][2]uint64{{2, 1}, {4, 0}, {6, 2}, {8, 0}, {10, 1}},
		[][2]uint64{{1, 2}, {4, 0}, {9, 1}},
		2,
	))
	for key, country := range map[uint64]uint8{
		0: 0, 1: 0, 2: 1, 3: 1, 4: 0, 5: 0, 6: 2, 7: 2, 8: 0, 9: 0, 10: 1, 1<<24 - 1: 1,
	} {
		if actual := store.lookup(key, true); actual != country {
			testContext.Errorf("v4 key %d 返回 %d，期望 %d", key, actual, country)
		}
	}
	for key, country := range map[uint64]uint8{0: 0, 1: 2, 3: 2, 4: 0, 8: 0, 9: 1, 1<<48 - 1: 1} {
		if actual := store.lookup(key|1<<63, false); actual != country {
			testContext.Errorf("v6 key %d 返回 %d，期望 %d", key, actual, country)
		}
	}
	if _, ok := store.cache.index[4]; !ok {
		testContext.Fatal("未分配间隙没有写入负缓存")
	}
	if unsafe.Sizeof(ipgeoCacheEntry{}) != 16 {
		testContext.Fatalf("LRU 节点大小为 %d，期望 16", unsafe.Sizeof(ipgeoCacheEntry{}))
	}
}

func TestClientIPGeoSyntheticEmptyAndLRU(testContext *testing.T) {
	empty := newIPGeoStore(buildTestIPGeoData(nil, nil, nil, 1))
	if actual := empty.lookup(123, true); actual != 0 {
		testContext.Fatalf("空 v4 表返回 %d", actual)
	}
	if actual := empty.lookup(456|1<<63, false); actual != 0 {
		testContext.Fatalf("空 v6 表返回 %d", actual)
	}

	v4Store := newIPGeoStore(buildTestIPGeoData([]string{"US"}, [][2]uint64{{0, 1}}, nil, 1))
	for key := uint64(0); key < ipgeoCacheCapacity; key++ {
		v4Store.lookup(key, true)
	}
	v4Store.lookup(0, true)
	for key := uint64(ipgeoCacheCapacity); key <= ipgeoCacheCapacity+2; key++ {
		v4Store.lookup(key, true)
	}
	for _, key := range []uint64{1, 2, 3} {
		if _, ok := v4Store.cache.index[key]; ok {
			testContext.Fatalf("v4 LRU 未按顺序淘汰 %d", key)
		}
	}
	for _, key := range []uint64{0, 1023, 1024, 1025, 1026} {
		if _, ok := v4Store.cache.index[key]; !ok {
			testContext.Fatalf("v4 LRU 意外淘汰 %d", key)
		}
	}
	assertIPGeoCache(testContext, &v4Store.cache)

	v6Store := newIPGeoStore(buildTestIPGeoData([]string{"US"}, nil, [][2]uint64{{0, 1}}, 1))
	for key := uint64(0); key <= ipgeoCacheCapacity-1; key++ {
		v6Store.lookup(key|1<<63, false)
	}
	v6Store.lookup(1<<63, false)
	for key := uint64(ipgeoCacheCapacity); key <= ipgeoCacheCapacity+2; key++ {
		v6Store.lookup(key|1<<63, false)
	}
	for _, key := range []uint64{1, 2, 3} {
		if _, ok := v6Store.cache.index[key|1<<63]; ok {
			testContext.Fatalf("v6 LRU 未按顺序淘汰 %d", key)
		}
	}
	for _, key := range []uint64{0, 1023, 1024, 1025, 1026} {
		if _, ok := v6Store.cache.index[key|1<<63]; !ok {
			testContext.Fatalf("v6 LRU 意外淘汰 %d", key)
		}
	}
	assertIPGeoCache(testContext, &v6Store.cache)
}

func assertIPGeoCache(testContext *testing.T, cache *ipgeoCache) {
	testContext.Helper()
	if len(cache.index) == 0 {
		if cache.head != -1 || cache.tail != -1 {
			testContext.Fatalf("空 LRU 的首尾为 %d/%d", cache.head, cache.tail)
		}
		return
	}
	if cache.head < 0 || cache.tail < 0 || cache.entries[cache.head].prev != -1 || cache.entries[cache.tail].next != -1 {
		testContext.Fatalf("LRU 首尾链接错误 head=%d tail=%d", cache.head, cache.tail)
	}
	seen := make(map[int16]bool, len(cache.index))
	position := cache.head
	previous := int16(-1)
	forwardCount := 0
	for position >= 0 {
		if seen[position] {
			testContext.Fatalf("LRU 正向遍历成环于 %d", position)
		}
		seen[position] = true
		entry := cache.entries[position]
		if entry.prev != previous {
			testContext.Fatalf("LRU 节点 %d 的 prev 为 %d，期望 %d", position, entry.prev, previous)
		}
		mapped, ok := cache.index[entry.key]
		if !ok || int16(mapped) != position {
			testContext.Fatalf("LRU 节点 %d 与索引不一致", position)
		}
		previous = position
		position = entry.next
		forwardCount++
	}
	if previous != cache.tail || forwardCount != len(cache.index) {
		testContext.Fatalf("LRU 正向长度为 %d，索引为 %d，尾节点为 %d/%d", forwardCount, len(cache.index), previous, cache.tail)
	}
	reverseSeen := make(map[int16]bool, len(cache.index))
	position = cache.tail
	next := int16(-1)
	backwardCount := 0
	for position >= 0 {
		if reverseSeen[position] {
			testContext.Fatalf("LRU 反向遍历成环于 %d", position)
		}
		reverseSeen[position] = true
		entry := cache.entries[position]
		if entry.next != next {
			testContext.Fatalf("LRU 节点 %d 的 next 为 %d，期望 %d", position, entry.next, next)
		}
		next = position
		position = entry.prev
		backwardCount++
	}
	if next != cache.head || backwardCount != len(cache.index) {
		testContext.Fatalf("LRU 反向长度为 %d，索引为 %d，首节点为 %d/%d", backwardCount, len(cache.index), next, cache.head)
	}
}

func TestClientIPGeoHeap(testContext *testing.T) {
	if os.Getenv("SBPM_IPGEO_HEAP_TEST") != "1" {
		command := exec.Command(os.Args[0], "-test.run=^TestClientIPGeoHeap$", "-test.count=1", "-test.v")
		command.Env = append(os.Environ(), "SBPM_IPGEO_HEAP_TEST=1")
		output, err := command.CombinedOutput()
		if err != nil {
			testContext.Fatalf("堆内存测量失败：%v\n%s", err, output)
		}
		testContext.Log(string(output))
		return
	}
	var before, private, loaded, firstFull, full runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if actual := LookupClientIPGeo("192.168.1.1"); !actual.IsPrivate {
		testContext.Fatalf("私网预热返回 %+v", actual)
	}
	runtime.GC()
	runtime.ReadMemStats(&private)
	LookupClientIPGeo("8.8.8.8")
	runtime.GC()
	runtime.ReadMemStats(&loaded)
	exerciseIPGeoMisses(ipgeoStore, 1500)
	runtime.GC()
	runtime.ReadMemStats(&firstFull)
	for round := 0; round < 3; round++ {
		exerciseIPGeoMisses(ipgeoStore, 1500)
	}
	runtime.GC()
	runtime.ReadMemStats(&full)
	fmt.Printf("ipgeo heap before=%d/%d private=%d/%d loaded=%d/%d first_full=%d/%d full=%d/%d\n", before.HeapAlloc, before.HeapInuse, private.HeapAlloc, private.HeapInuse, loaded.HeapAlloc, loaded.HeapInuse, firstFull.HeapAlloc, firstFull.HeapInuse, full.HeapAlloc, full.HeapInuse)
	if full.HeapAlloc-private.HeapAlloc >= 256*1024 {
		testContext.Fatalf("IP 地区表新增堆内存 %d 字节，超过 256KB", full.HeapAlloc-private.HeapAlloc)
	}
	if full.HeapAlloc > firstFull.HeapAlloc+64*1024 {
		testContext.Fatalf("连续 miss 与淘汰后堆内存增长 %d 字节", full.HeapAlloc-firstFull.HeapAlloc)
	}
}

func exerciseIPGeoMisses(store *ipgeoDataStore, count int) {
	for index := 0; index < count; index++ {
		block := store.v4Blocks[index%len(store.v4Blocks)]
		key := uint64(block.firstKey) + uint64(index/len(store.v4Blocks))
		store.lookup(key, true)
	}
	for index := 0; index < count; index++ {
		block := store.v6Blocks[index%len(store.v6Blocks)]
		key := block.firstKey + uint64(index/len(store.v6Blocks))
		store.lookup(key|1<<63, false)
	}
}

func BenchmarkClientIPGeoHit(benchmark *testing.B) {
	LookupClientIPGeo("8.8.8.8")
	benchmark.ReportAllocs()
	benchmark.ResetTimer()
	for index := 0; index < benchmark.N; index++ {
		LookupClientIPGeo("8.8.8.8")
	}
}

func BenchmarkClientIPGeoMiss(benchmark *testing.B) {
	LookupClientIPGeo("8.8.8.8")
	store := ipgeoStore
	baseKey := uint64(store.v4Blocks[0].firstKey)
	benchmark.ReportAllocs()
	benchmark.ResetTimer()
	for index := 0; index < benchmark.N; index++ {
		store.lookup(baseKey+uint64(index&2047), true)
	}
}

func buildTestIPGeoData(countries []string, v4, v6 [][2]uint64, blockSize int) string {
	v4Blocks := buildTestBlocks(v4, blockSize)
	v6Blocks := buildTestBlocks(v6, blockSize)
	indexSize := len(v4Blocks)*8 + len(v6Blocks)*12
	offset := 10 + len(countries)*2 + indexSize + 4
	indexes := make([]byte, 0, indexSize)
	payload := make([]byte, 0)
	for _, block := range v4Blocks {
		indexes = appendTestIndex(indexes, block.firstKey, uint32(offset), false)
		payload = append(payload, block.compressed...)
		offset += len(block.compressed)
	}
	for _, block := range v6Blocks {
		indexes = appendTestIndex(indexes, block.firstKey, uint32(offset), true)
		payload = append(payload, block.compressed...)
		offset += len(block.compressed)
	}
	data := make([]byte, 0, offset)
	data = append(data, "SBG2"...)
	data = append(data, byte(len(countries)), 0)
	data = appendTestUint16(data, uint16(len(v4Blocks)))
	data = appendTestUint16(data, uint16(len(v6Blocks)))
	for _, country := range countries {
		data = append(data, country...)
	}
	data = append(data, indexes...)
	data = appendTestUint32(data, uint32(offset))
	data = append(data, payload...)
	return string(data)
}

type testIPGeoBlock struct {
	firstKey   uint64
	compressed []byte
}

func buildTestBlocks(transitions [][2]uint64, blockSize int) []testIPGeoBlock {
	blocks := make([]testIPGeoBlock, 0, (len(transitions)+blockSize-1)/blockSize)
	for start := 0; start < len(transitions); start += blockSize {
		end := start + blockSize
		if end > len(transitions) {
			end = len(transitions)
		}
		raw := []byte{byte(transitions[start][1])}
		previousKey := transitions[start][0]
		for _, transition := range transitions[start+1 : end] {
			raw = appendTestUvarint(raw, transition[0]-previousKey)
			raw = append(raw, byte(transition[1]))
			previousKey = transition[0]
		}
		var compressed bytes.Buffer
		writer, err := flate.NewWriter(&compressed, flate.BestCompression)
		if err != nil {
			panic(err)
		}
		if _, err := writer.Write(raw); err != nil {
			panic(err)
		}
		if err := writer.Close(); err != nil {
			panic(err)
		}
		blocks = append(blocks, testIPGeoBlock{firstKey: transitions[start][0], compressed: compressed.Bytes()})
	}
	return blocks
}

func appendTestIndex(data []byte, firstKey uint64, offset uint32, v6 bool) []byte {
	if v6 {
		data = appendTestUint64(data, firstKey)
	} else {
		data = appendTestUint32(data, uint32(firstKey))
	}
	return appendTestUint32(data, offset)
}

func appendTestUvarint(data []byte, value uint64) []byte {
	for value >= 0x80 {
		data = append(data, byte(value)|0x80)
		value >>= 7
	}
	return append(data, byte(value))
}

func appendTestUint16(data []byte, value uint16) []byte {
	var encoded [2]byte
	binary.BigEndian.PutUint16(encoded[:], value)
	return append(data, encoded[:]...)
}

func appendTestUint32(data []byte, value uint32) []byte {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	return append(data, encoded[:]...)
}

func appendTestUint64(data []byte, value uint64) []byte {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	return append(data, encoded[:]...)
}
