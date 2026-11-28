package services

import (
	"bufio"
	"compress/flate"
	_ "embed"
	"encoding/binary"
	"io"
	"net/netip"
	"strings"
	"sync"
)

type ClientIPGeo struct {
	CountryCode string
	IsPrivate   bool
}

//go:embed ipgeo_data.bin
var ipgeoData string

const ipgeoCacheCapacity = 1024

type ipgeoV4Block struct {
	firstKey uint32
	offset   uint32
}

type ipgeoV6Block struct {
	firstKey uint64
	offset   uint32
}

type ipgeoCacheEntry struct {
	key     uint64
	prev    int16
	next    int16
	country uint8
}

type ipgeoCache struct {
	entries []ipgeoCacheEntry
	index   map[uint64]uint16
	head    int16
	tail    int16
}

type ipgeoDataStore struct {
	data       string
	countries  string
	v4Blocks   []ipgeoV4Block
	v6Blocks   []ipgeoV6Block
	cache      ipgeoCache
	cacheMu    sync.Mutex
	decodeMu   sync.Mutex
	source     strings.Reader
	decoder    io.ReadCloser
	resetter   flate.Resetter
	byteReader *bufio.Reader
}

var (
	ipgeoOnce   sync.Once
	ipgeoStore  *ipgeoDataStore
	cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")
)

func LookupClientIPGeo(raw string) ClientIPGeo {
	ipv6Syntax := false
	if len(raw) > 1 && raw[0] == '[' && raw[len(raw)-1] == ']' {
		raw, ipv6Syntax = raw[1:len(raw)-1], true
	}
	for index := 0; index < len(raw); index++ {
		if raw[index] == '%' {
			if index == len(raw)-1 {
				return ClientIPGeo{}
			}
			raw, ipv6Syntax = raw[:index], true
			break
		}
	}
	ip, err := netip.ParseAddr(raw)
	if err != nil || ipv6Syntax && !ip.Is6() {
		return ClientIPGeo{}
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || cgnatPrefix.Contains(ip) {
		return ClientIPGeo{IsPrivate: true}
	}
	ipgeoOnce.Do(loadIPGeo)
	store := ipgeoStore
	var countryIndex uint8
	if ip.Is4() {
		address := ip.As4()
		key := uint64(binary.BigEndian.Uint32(address[:]) >> 8)
		countryIndex = store.lookup(key, true)
	} else {
		address := ip.As16()
		key := binary.BigEndian.Uint64(address[:8]) >> 16
		countryIndex = store.lookup(key|1<<63, false)
	}
	if countryIndex == 0 {
		return ClientIPGeo{}
	}
	offset := (int(countryIndex) - 1) * 2
	return ClientIPGeo{CountryCode: store.countries[offset : offset+2]}
}

func (store *ipgeoDataStore) lookup(key uint64, v4 bool) uint8 {
	store.cacheMu.Lock()
	if country, ok := store.cache.get(key); ok {
		store.cacheMu.Unlock()
		return country
	}
	store.cacheMu.Unlock()

	store.decodeMu.Lock()
	var country uint8
	if v4 {
		country = store.decodeV4(uint32(key))
	} else {
		country = store.decodeV6(key &^ (uint64(1) << 63))
	}
	store.decodeMu.Unlock()

	store.cacheMu.Lock()
	store.cache.put(key, country)
	store.cacheMu.Unlock()
	return country
}

func (cache *ipgeoCache) get(key uint64) (uint8, bool) {
	position, ok := cache.index[key]
	if !ok {
		return 0, false
	}
	cache.moveToFront(int16(position))
	return cache.entries[position].country, true
}

func (cache *ipgeoCache) put(key uint64, country uint8) {
	if position, ok := cache.index[key]; ok {
		cache.entries[position].country = country
		cache.moveToFront(int16(position))
		return
	}
	var position int16
	if len(cache.index) < len(cache.entries) {
		position = int16(len(cache.index))
	} else {
		position = cache.tail
		delete(cache.index, cache.entries[position].key)
		entry := &cache.entries[position]
		entry.key = key
		entry.country = country
		cache.index[key] = uint16(position)
		cache.moveToFront(position)
		return
	}
	entry := &cache.entries[position]
	entry.key = key
	entry.country = country
	entry.prev = -1
	entry.next = cache.head
	if cache.head >= 0 {
		cache.entries[cache.head].prev = position
	} else {
		cache.tail = position
	}
	cache.head = position
	cache.index[key] = uint16(position)
}

func (cache *ipgeoCache) moveToFront(position int16) {
	if position == cache.head {
		return
	}
	entry := &cache.entries[position]
	if entry.prev >= 0 {
		cache.entries[entry.prev].next = entry.next
	}
	if entry.next >= 0 {
		cache.entries[entry.next].prev = entry.prev
	} else {
		cache.tail = entry.prev
	}
	entry.prev = -1
	entry.next = cache.head
	cache.entries[cache.head].prev = position
	cache.head = position
}

func (store *ipgeoDataStore) decodeV4(key uint32) uint8 {
	index := findV4Block(store.v4Blocks, key)
	if index < 0 {
		return 0
	}
	end := len(store.data)
	if index+1 < len(store.v4Blocks) {
		end = int(store.v4Blocks[index+1].offset)
	} else if len(store.v6Blocks) > 0 {
		end = int(store.v6Blocks[0].offset)
	}
	return store.decode(uint64(store.v4Blocks[index].firstKey), store.v4Blocks[index].offset, uint32(end), uint64(key))
}

func (store *ipgeoDataStore) decodeV6(key uint64) uint8 {
	index := findV6Block(store.v6Blocks, key)
	if index < 0 {
		return 0
	}
	end := len(store.data)
	if index+1 < len(store.v6Blocks) {
		end = int(store.v6Blocks[index+1].offset)
	}
	return store.decode(store.v6Blocks[index].firstKey, store.v6Blocks[index].offset, uint32(end), key)
}

func (store *ipgeoDataStore) decode(firstKey uint64, offset uint32, end uint32, key uint64) uint8 {
	store.source.Reset(store.data[int(offset):int(end)])
	if err := store.resetter.Reset(&store.source, nil); err != nil {
		panic("嵌入式 IP 地区表压缩块错误")
	}
	store.byteReader.Reset(store.decoder)
	country, err := store.byteReader.ReadByte()
	if err != nil {
		panic("嵌入式 IP 地区表块为空")
	}
	currentKey := firstKey
	for {
		delta, err := binary.ReadUvarint(store.byteReader)
		if err == io.EOF {
			return country
		}
		if err != nil {
			panic("嵌入式 IP 地区表块损坏")
		}
		nextKey := currentKey + delta
		if nextKey > key {
			return country
		}
		nextCountry, err := store.byteReader.ReadByte()
		if err != nil {
			panic("嵌入式 IP 地区表块损坏")
		}
		currentKey = nextKey
		country = nextCountry
	}
}

func findV4Block(blocks []ipgeoV4Block, key uint32) int {
	low, high := 0, len(blocks)
	for low < high {
		middle := low + (high-low)/2
		if blocks[middle].firstKey <= key {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low - 1
}

func findV6Block(blocks []ipgeoV6Block, key uint64) int {
	low, high := 0, len(blocks)
	for low < high {
		middle := low + (high-low)/2
		if blocks[middle].firstKey <= key {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low - 1
}

func loadIPGeo() {
	ipgeoStore = newIPGeoStore(ipgeoData)
}

func newIPGeoStore(raw string) *ipgeoDataStore {
	if len(raw) < 10 || raw[:4] != "SBG2" || raw[5] != 0 {
		panic("嵌入式 IP 地区表格式错误")
	}
	countryCount := int(raw[4])
	v4Count := int(binary.BigEndian.Uint16([]byte(raw[6:8])))
	v6Count := int(binary.BigEndian.Uint16([]byte(raw[8:10])))
	position := 10
	countryEnd := position + countryCount*2
	if countryEnd > len(raw) {
		panic("嵌入式 IP 地区表头错误")
	}
	countries := raw[position:countryEnd]
	position = countryEnd
	indexEnd := position + v4Count*8 + v6Count*12
	if indexEnd > len(raw)-4 {
		panic("嵌入式 IP 地区表索引错误")
	}
	store := &ipgeoDataStore{
		data:      raw,
		countries: countries,
		v4Blocks:  make([]ipgeoV4Block, v4Count),
		v6Blocks:  make([]ipgeoV6Block, v6Count),
		cache: ipgeoCache{
			entries: make([]ipgeoCacheEntry, ipgeoCacheCapacity),
			index:   make(map[uint64]uint16, ipgeoCacheCapacity),
			head:    -1,
			tail:    -1,
		},
	}
	endOffset := binary.BigEndian.Uint32([]byte(raw[indexEnd : indexEnd+4]))
	if int(endOffset) != len(raw) || int(endOffset) < indexEnd+4 {
		panic("嵌入式 IP 地区表结束偏移错误")
	}
	payloadStart := uint32(indexEnd + 4)
	previousOffset := payloadStart
	hasPreviousOffset := false
	for index := range store.v4Blocks {
		firstKey := binary.BigEndian.Uint32([]byte(raw[position : position+4]))
		offset := binary.BigEndian.Uint32([]byte(raw[position+4 : position+8]))
		if firstKey >= 1<<24 || offset < payloadStart || offset >= endOffset || hasPreviousOffset && offset <= previousOffset || index > 0 && firstKey <= store.v4Blocks[index-1].firstKey {
			panic("嵌入式 IP 地区表 v4 索引错误")
		}
		store.v4Blocks[index] = ipgeoV4Block{firstKey: firstKey, offset: offset}
		previousOffset = offset
		hasPreviousOffset = true
		position += 8
	}
	for index := range store.v6Blocks {
		firstKey := binary.BigEndian.Uint64([]byte(raw[position : position+8]))
		offset := binary.BigEndian.Uint32([]byte(raw[position+8 : position+12]))
		if firstKey >= 1<<48 || offset < payloadStart || offset >= endOffset || hasPreviousOffset && offset <= previousOffset || index > 0 && firstKey <= store.v6Blocks[index-1].firstKey {
			panic("嵌入式 IP 地区表 v6 索引错误")
		}
		store.v6Blocks[index] = ipgeoV6Block{firstKey: firstKey, offset: offset}
		previousOffset = offset
		hasPreviousOffset = true
		position += 12
	}
	store.source.Reset("")
	store.decoder = flate.NewReader(&store.source)
	store.resetter = store.decoder.(flate.Resetter)
	store.byteReader = bufio.NewReaderSize(store.decoder, 2048)
	return store
}
