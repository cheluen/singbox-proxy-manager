package services

import (
	"os"
	"os/exec"
	"sync"
	"testing"
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
	if ipgeoDB.countries != "" || ipgeoDB.v4 != nil || ipgeoDB.v6 != nil {
		testContext.Fatal("内网或非法地址触发了地区表加载")
	}
	testContext.Run("ConcurrentInitialization", testClientIPGeoConcurrent)
	if ipgeoDB.countries == "" || len(ipgeoDB.v4) == 0 || len(ipgeoDB.v6) == 0 {
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
