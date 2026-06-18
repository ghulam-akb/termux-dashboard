package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type BatteryInfo struct {
	Percentage  int     `json:"percentage"`
	Temperature float64 `json:"temperature"`
	Health      string  `json:"health"`
	Plugged     string  `json:"plugged"`
	Status      string  `json:"status"`
}

type RAMInfo struct {
	Total     int `json:"total"`
	Used      int `json:"used"`
	Free      int `json:"free"`
	Available int `json:"available"`
}

type StorageInfo struct {
	Size    string `json:"size"`
	Used    string `json:"used"`
	Avail   string `json:"avail"`
	Percent string `json:"percent"`
}

type CPUInfo struct {
	Usage float64 `json:"usage"`
	Model string  `json:"model"`
	Cores int     `json:"cores"`
}

type NetInterface struct {
	Name string   `json:"name"`
	IPs  []string `json:"ips"`
}

type SystemInfo struct {
	OS     string         `json:"os"`
	Kernel string         `json:"kernel"`
	Model  string         `json:"model"`
	NetIf  []NetInterface `json:"net_interfaces"`
}

type StatsResponse struct {
	Battery BatteryInfo `json:"battery"`
	RAM     RAMInfo     `json:"ram"`
	Storage StorageInfo `json:"storage"`
	Uptime  string      `json:"uptime"`
	CPU     CPUInfo     `json:"cpu"`
	System  SystemInfo  `json:"system"`
	NetRX   float64     `json:"net_rx"`
	NetTX   float64     `json:"net_tx"`
}

var (
	globalCPUUsage      float64
	globalCPUUsageMutex sync.RWMutex

	// Network Traffic sampling
	prevNetTime  time.Time
	prevRxBytes  uint64
	prevTxBytes  uint64
	netSpeedMutex sync.Mutex
	currRxSpeed  float64
	currTxSpeed  float64
)

func getNetworkSpeeds() (float64, float64) {
	netSpeedMutex.Lock()
	defer netSpeedMutex.Unlock()
	return currRxSpeed, currTxSpeed
}

func updateNetworkSpeeds() {
	content, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return
	}

	lines := strings.Split(string(content), "\n")
	var totalRx uint64
	var totalTx uint64

	for _, line := range lines {
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) < 2 {
			continue
		}
		iface := strings.TrimSpace(parts[0])
		if iface == "lo" {
			continue
		}

		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}

		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)

		totalRx += rx
		totalTx += tx
	}

	netSpeedMutex.Lock()
	defer netSpeedMutex.Unlock()

	now := time.Now()
	if !prevNetTime.IsZero() {
		duration := now.Sub(prevNetTime).Seconds()
		if duration > 0 {
			currRxSpeed = float64(totalRx-prevRxBytes) / 1024.0 / duration
			currTxSpeed = float64(totalTx-prevTxBytes) / 1024.0 / duration
		}
	}
	prevNetTime = now
	prevRxBytes = totalRx
	prevTxBytes = totalTx
}

func acquireWakeLock() {
	_, err := exec.LookPath("termux-wake-lock")
	if err == nil {
		if err := exec.Command("termux-wake-lock").Run(); err == nil {
			fmt.Println("[INFO] Android Wake Lock diaktifkan (Mencegah CPU tidur).")
		} else {
			fmt.Printf("[WARNING] Gagal mengaktifkan Wake Lock: %v\n", err)
		}
	}
}

func releaseWakeLock() {
	_, err := exec.LookPath("termux-wake-unlock")
	if err == nil {
		if err := exec.Command("termux-wake-unlock").Run(); err == nil {
			fmt.Println("[INFO] Android Wake Lock dinonaktifkan.")
		} else {
			fmt.Printf("[WARNING] Gagal menonaktifkan Wake Lock: %v\n", err)
		}
	}
}

func watchSystemMetrics(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	var batteryAlertSent bool
	var storageAlertSent bool

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Check battery temperature
			bat := getBatteryInfo()
			if bat.Temperature >= 45.0 {
				if !batteryAlertSent {
					sendNotification("⚠️ Baterai Panas!", fmt.Sprintf("Suhu baterai HP Anda mencapai %.1f°C.", bat.Temperature))
					batteryAlertSent = true
				}
			} else {
				batteryAlertSent = false
			}

			// Check storage
			storage := getStorageInfo()
			pctStr := strings.TrimSuffix(storage.Percent, "%")
			if pct, err := strconv.Atoi(pctStr); err == nil {
				if pct >= 95 { // Storage > 95% used
					if !storageAlertSent {
						sendNotification("⚠️ Penyimpanan Penuh!", fmt.Sprintf("Penyimpanan internal hampir habis: %s terpakai (%s tersisa).", storage.Percent, storage.Avail))
						storageAlertSent = true
					}
				} else {
					storageAlertSent = false
				}
			}
		}
	}
}

func sendNotification(title, content string) {
	_, err := exec.LookPath("termux-notification")
	if err == nil {
		exec.Command("termux-notification", "--id", "dashboard-alert", "--title", title, "--content", content).Run()
	}
}

func getBatteryInfo() BatteryInfo {
	info := BatteryInfo{Status: "Unavailable", Health: "UNKNOWN", Plugged: "UNPLUGGED"}

	cmd := exec.Command("termux-battery-status")
	if output, err := cmd.Output(); err == nil {
		var result map[string]interface{}
		if err := json.Unmarshal(output, &result); err == nil {
			if pct, ok := result["percentage"].(float64); ok {
				info.Percentage = int(pct)
			}
			if temp, ok := result["temperature"].(float64); ok {
				info.Temperature = temp
			}
			if health, ok := result["health"].(string); ok {
				info.Health = health
			}
			if status, ok := result["status"].(string); ok {
				info.Status = status
			}
			if plug, ok := result["plugged"].(string); ok {
				info.Plugged = plug
			}
			return info
		}
	}

	if content, err := os.ReadFile("/sys/class/power_supply/battery/capacity"); err == nil {
		if val, err := strconv.Atoi(strings.TrimSpace(string(content))); err == nil {
			info.Percentage = val
		}
	}
	if content, err := os.ReadFile("/sys/class/power_supply/battery/temp"); err == nil {
		if val, err := strconv.ParseFloat(strings.TrimSpace(string(content)), 64); err == nil {
			if val > 100 {
				info.Temperature = val / 10.0
			} else {
				info.Temperature = val
			}
		}
	} else if content, err := os.ReadFile("/sys/class/power_supply/battery/temperature"); err == nil {
		if val, err := strconv.ParseFloat(strings.TrimSpace(string(content)), 64); err == nil {
			if val > 100 {
				info.Temperature = val / 10.0
			} else {
				info.Temperature = val
			}
		}
	}
	if content, err := os.ReadFile("/sys/class/power_supply/battery/status"); err == nil {
		info.Status = strings.TrimSpace(string(content))
		if strings.ToLower(info.Status) == "charging" {
			info.Plugged = "PLUGGED"
		}
	}
	if content, err := os.ReadFile("/sys/class/power_supply/battery/health"); err == nil {
		info.Health = strings.ToUpper(strings.TrimSpace(string(content)))
	}

	return info
}

func getRAMInfo() RAMInfo {
	info := RAMInfo{}

	content, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return info
	}

	lines := strings.Split(string(content), "\n")
	var memTotal, memFree, memAvailable int

	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key := parts[0]
		val, _ := strconv.Atoi(parts[1])

		switch key {
		case "MemTotal:":
			memTotal = val / 1024
		case "MemFree:":
			memFree = val / 1024
		case "MemAvailable:":
			memAvailable = val / 1024
		}
	}

	info.Total = memTotal
	info.Free = memFree
	
	if memAvailable == 0 {
		info.Available = memFree
	} else {
		info.Available = memAvailable
	}
	
	info.Used = memTotal - info.Available
	return info
}

func getStorageInfo() StorageInfo {
	info := StorageInfo{Size: "0", Used: "0", Avail: "0", Percent: "0%"}

	var stat syscall.Statfs_t
	err := syscall.Statfs("/data", &stat)
	if err != nil {
		err = syscall.Statfs("/", &stat)
	}

	if err == nil {
		total := stat.Blocks * uint64(stat.Bsize)
		free := stat.Bfree * uint64(stat.Bsize)
		avail := stat.Bavail * uint64(stat.Bsize)
		used := total - free

		totalGB := float64(total) / (1024 * 1024 * 1024)
		usedGB := float64(used) / (1024 * 1024 * 1024)
		availGB := float64(avail) / (1024 * 1024 * 1024)
		
		var percent float64
		if total > 0 {
			percent = (float64(used) / float64(total)) * 100
		}

		info.Size = fmt.Sprintf("%.1f GB", totalGB)
		info.Used = fmt.Sprintf("%.1f GB", usedGB)
		info.Avail = fmt.Sprintf("%.1f GB", availGB)
		info.Percent = fmt.Sprintf("%.0f%%", percent)
	}

	return info
}

func getUptimeInfo() string {
	cmd := exec.Command("uptime")
	if output, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(output))
	}

	content, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "Unavailable"
	}

	parts := strings.Fields(string(content))
	if len(parts) > 0 {
		uptimeSecs, err := strconv.ParseFloat(parts[0], 64)
		if err == nil {
			duration := time.Duration(uptimeSecs) * time.Second
			return fmt.Sprintf("up %v", duration.Truncate(time.Second))
		}
	}

	return "Unavailable"
}

func getCPUUsageFromTop() (float64, error) {
	cmd := exec.Command("top", "-n", "1", "-b")
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "%cpu") && strings.Contains(line, "%idle") {
			fields := strings.Fields(line)
			var totalCPU float64 = 100.0
			var idleCPU float64 = 100.0

			for _, field := range fields {
				if strings.HasSuffix(field, "%cpu") {
					valStr := strings.TrimSuffix(field, "%cpu")
					if val, err := strconv.ParseFloat(valStr, 64); err == nil {
						totalCPU = val
					}
				} else if strings.HasSuffix(field, "%idle") {
					valStr := strings.TrimSuffix(field, "%idle")
					if val, err := strconv.ParseFloat(valStr, 64); err == nil {
						idleCPU = val
					}
				}
			}

			if totalCPU > 0 {
				usage := ((totalCPU - idleCPU) / totalCPU) * 100.0
				if usage < 0 {
					return 0, nil
				}
				if usage > 100 {
					return 100, nil
				}
				return usage, nil
			}
		}
	}
	return 0, fmt.Errorf("could not find CPU metrics in top output")
}

func watchCPUUsage(ctx context.Context) {
	// Initialize immediately
	if usage, err := getCPUUsageFromTop(); err == nil {
		globalCPUUsageMutex.Lock()
		globalCPUUsage = usage
		globalCPUUsageMutex.Unlock()
	}
	updateNetworkSpeeds()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			usage, err := getCPUUsageFromTop()
			if err == nil {
				globalCPUUsageMutex.Lock()
				globalCPUUsage = usage
				globalCPUUsageMutex.Unlock()
			}
			updateNetworkSpeeds()
		}
	}
}

func getCPUInfo() CPUInfo {
	globalCPUUsageMutex.RLock()
	usage := globalCPUUsage
	globalCPUUsageMutex.RUnlock()

	brand := getAndroidProp("ro.soc.manufacturer")
	model := getAndroidProp("ro.soc.model")
	cpuModel := strings.TrimSpace(brand + " " + model)
	if cpuModel == "" {
		cpuModel = "ARM64 Processor"
	}

	return CPUInfo{
		Usage: usage,
		Model: cpuModel,
		Cores: runtime.NumCPU(),
	}
}

func getSystemInfo() SystemInfo {
	brand := getAndroidProp("ro.product.brand")
	model := getAndroidProp("ro.product.model")
	deviceModel := strings.TrimSpace(brand + " " + model)
	if deviceModel == "" {
		deviceModel = "Android Device"
	}

	androidVer := getAndroidProp("ro.build.version.release")
	osName := "Android"
	if androidVer != "" {
		osName = "Android " + androidVer
	}

	kernelVer := ""
	cmd := exec.Command("uname", "-r")
	if val, err := cmd.Output(); err == nil {
		kernelVer = strings.TrimSpace(string(val))
	}
	if kernelVer == "" {
		kernelVer = "Linux"
	}

	return SystemInfo{
		OS:     osName,
		Kernel: kernelVer,
		Model:  deviceModel,
		NetIf:  getNetworkInterfaces(),
	}
}

func getNetworkInterfaces() []NetInterface {
	var list []NetInterface
	ifaces, err := net.Interfaces()
	if err != nil {
		return list
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		var ips []string
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil {
				continue
			}
			if ip.IsLoopback() {
				continue
			}
			ips = append(ips, ip.String())
		}

		if len(ips) > 0 {
			list = append(list, NetInterface{
				Name: iface.Name,
				IPs:  ips,
			})
		}
	}
	return list
}

func getAndroidProp(key string) string {
	cmd := exec.Command("getprop", key)
	if val, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(val))
	}
	return ""
}
