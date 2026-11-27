package services

import (
	"compress/gzip"
	"embed"
	"encoding/binary"
	"net/netip"
	"sort"
	"sync"
)

type ClientIPGeo struct {
	CountryCode string
	IsPrivate   bool
}

//go:embed ipgeo_data.bin.gz
var ipgeoData embed.FS

var (
	ipgeoOnce sync.Once
	ipgeoDB   struct {
		countries string
		v4        []uint32
		v6        []uint64
	}
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
	var countryIndex uint8
	if ip.Is4() {
		address := ip.As4()
		key := binary.BigEndian.Uint32(address[:]) >> 8
		index := sort.Search(len(ipgeoDB.v4), func(index int) bool { return ipgeoDB.v4[index]>>8 > key }) - 1
		if index >= 0 {
			countryIndex = uint8(ipgeoDB.v4[index])
		}
	} else {
		address := ip.As16()
		key := binary.BigEndian.Uint64(address[:8]) >> 16
		index := sort.Search(len(ipgeoDB.v6), func(index int) bool { return ipgeoDB.v6[index]>>16 > key }) - 1
		if index >= 0 {
			countryIndex = uint8(ipgeoDB.v6[index])
		}
	}
	if countryIndex == 0 {
		return ClientIPGeo{}
	}
	offset := (int(countryIndex) - 1) * 2
	return ClientIPGeo{CountryCode: ipgeoDB.countries[offset : offset+2]}
}

type ipgeoReader struct {
	*gzip.Reader
	byteBuffer [1]byte
}

func (reader *ipgeoReader) ReadByte() (byte, error) {
	count, err := reader.Read(reader.byteBuffer[:])
	if count == 1 {
		return reader.byteBuffer[0], nil
	}
	return 0, err
}

func (reader *ipgeoReader) readVarint() uint64 {
	value, err := binary.ReadUvarint(reader)
	if err != nil {
		panic(err)
	}
	return value
}

func (reader *ipgeoReader) readIndex() uint8 {
	index, err := reader.ReadByte()
	if err != nil {
		panic(err)
	}
	return index
}

func loadIPGeo() {
	compressed, err := ipgeoData.Open("ipgeo_data.bin.gz")
	if err != nil {
		panic(err)
	}
	defer compressed.Close()
	stream, err := gzip.NewReader(compressed)
	if err != nil {
		panic(err)
	}
	defer stream.Close()
	reader := &ipgeoReader{Reader: stream}
	var header [4]byte
	if err = binary.Read(reader, binary.BigEndian, header[:]); err != nil {
		panic(err)
	}
	if string(header[:]) != "SBG1" {
		panic("嵌入式 IP 地区表格式错误")
	}
	countries := make([]byte, reader.readVarint()*2)
	if err = binary.Read(reader, binary.BigEndian, countries); err != nil {
		panic(err)
	}
	ipgeoDB.countries = string(countries)
	ipgeoDB.v4 = make([]uint32, reader.readVarint())
	var start uint64
	for index := range ipgeoDB.v4 {
		start += reader.readVarint()
		ipgeoDB.v4[index] = uint32(start)<<8 | uint32(reader.readIndex())
	}
	ipgeoDB.v6 = make([]uint64, reader.readVarint())
	start = 0
	for index := range ipgeoDB.v6 {
		start += reader.readVarint()
		ipgeoDB.v6[index] = start<<16 | uint64(reader.readIndex())
	}
}
