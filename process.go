package main

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

type ProcessInfo struct {
	PID     int     `json:"pid"`
	User    string  `json:"user"`
	CPU     float64 `json:"cpu"`
	MEM     float64 `json:"mem"`
	Command string  `json:"command"`
}

func getProcessList() ([]ProcessInfo, error) {
	cmd := exec.Command("ps", "-o", "pid,user,%cpu,%mem,args")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var list []ProcessInfo
	lines := strings.Split(string(output), "\n")
	if len(lines) < 2 {
		return list, nil
	}

	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		user := fields[1]
		cpu, _ := strconv.ParseFloat(fields[2], 64)
		mem, _ := strconv.ParseFloat(fields[3], 64)

		command := strings.Join(fields[4:], " ")

		list = append(list, ProcessInfo{
			PID:     pid,
			User:    user,
			CPU:     cpu,
			MEM:     mem,
			Command: command,
		})
	}

	return list, nil
}

func killProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
