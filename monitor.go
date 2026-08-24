package main

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type systemSampler struct {
	mu                       sync.Mutex
	lastCPUIdle, lastCPUTotal uint64
	lastNet                  map[string][2]uint64
	lastAt                   time.Time
}

func newSystemSampler() *systemSampler {
	return &systemSampler{lastNet: map[string][2]uint64{}}
}

func readCPU() (uint64, uint64) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil { return 0, 0 }
	fields := strings.Fields(strings.SplitN(string(b), "\n", 2)[0])
	if len(fields) < 5 { return 0, 0 }
	var total uint64
	for i := 1; i < len(fields); i++ { n, _ := strconv.ParseUint(fields[i], 10, 64); total += n }
	idle, _ := strconv.ParseUint(fields[4], 10, 64)
	if len(fields) > 5 { n, _ := strconv.ParseUint(fields[5], 10, 64); idle += n }
	return idle, total
}

func readMem() map[string]uint64 {
	out := map[string]uint64{}
	b, _ := os.ReadFile("/proc/meminfo")
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 { n, _ := strconv.ParseUint(fields[1], 10, 64); out[strings.TrimSuffix(fields[0], ":")] = n * 1024 }
	}
	return out
}

func readNet() map[string][2]uint64 {
	out := map[string][2]uint64{}
	f, err := os.Open("/proc/net/dev")
	if err != nil { return out }
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), ":")
		if len(parts) != 2 { continue }
		name := strings.TrimSpace(parts[0])
		if name == "lo" || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") { continue }
		fields := strings.Fields(parts[1])
		if len(fields) >= 9 { rx, _ := strconv.ParseUint(fields[0], 10, 64); tx, _ := strconv.ParseUint(fields[8], 10, 64); out[name] = [2]uint64{rx, tx} }
	}
	return out
}

func readDisk() (uint64, uint64) {
	out, err := exec.Command("df", "-B1", "/").Output()
	if err != nil { return 0, 0 }
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 { return 0, 0 }
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 3 { return 0, 0 }
	total, _ := strconv.ParseUint(fields[1], 10, 64)
	used, _ := strconv.ParseUint(fields[2], 10, 64)
	return total, used
}

func (s *systemSampler) Status(selected string, relay *Relay, started time.Time) map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	idle, total := readCPU()
	nets := readNet()
	cpu := float64(0)
	if s.lastCPUTotal > 0 && total > s.lastCPUTotal { cpu = 100 * (1 - float64(idle-s.lastCPUIdle)/float64(total-s.lastCPUTotal)) }
	elapsed := now.Sub(s.lastAt).Seconds()
	rxRate, txRate := float64(0), float64(0)
	if elapsed > 0 {
		for name, value := range nets {
			if selected != "" && selected != name { continue }
			if old, ok := s.lastNet[name]; ok { rxRate += float64(value[0]-old[0]) / elapsed; txRate += float64(value[1]-old[1]) / elapsed }
		}
	}
	s.lastCPUIdle, s.lastCPUTotal, s.lastNet, s.lastAt = idle, total, nets, now

	mem := readMem()
	memTotal := mem["MemTotal"]
	memUsed := memTotal - mem["MemAvailable"]
	diskTotal, diskUsed := readDisk()
	uptime := uint64(0)
	if b, _ := os.ReadFile("/proc/uptime"); len(b) > 0 { uptime, _ = strconv.ParseUint(strings.Split(string(b), ".")[0], 10, 64) }
	tcp, udp := relay.ActiveCounts()
	return map[string]interface{}{
		"interfaces": nets, "rx_bps": rxRate, "tx_bps": txRate, "cpu_percent": cpu, "cpu_cores": runtime.NumCPU(),
		"mem_total": memTotal, "mem_used": memUsed, "swap_total": mem["SwapTotal"], "swap_used": mem["SwapTotal"] - mem["SwapFree"],
		"disk_total": diskTotal, "disk_used": diskUsed, "tcp_connections": tcp, "udp_connections": udp,
		"uptime_seconds": uptime, "panel_uptime_seconds": uint64(time.Since(started).Seconds()),
	}
}
