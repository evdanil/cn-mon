//go:build linux

package probe

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func nativePermissionError(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

func nativePrivilegeFacts() string {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fmt.Sprintf("proc_status_error=%v", err)
	}
	var facts []string
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") || strings.HasPrefix(line, "Gid:") || strings.HasPrefix(line, "CapEff:") || strings.HasPrefix(line, "CapPrm:") || strings.HasPrefix(line, "CapBnd:") || strings.HasPrefix(line, "CapAmb:") {
			facts = append(facts, strings.TrimSpace(line))
		}
	}
	return strings.Join(facts, " ")
}

func nativeCanRaw() bool {
	return nativeCapabilityBits("CapEff")&(1<<13) != 0
}

func nativeRawGrantPresent() bool {
	return nativeCapabilityBits("CapEff")&(1<<13) != 0 || nativeCapabilityBits("CapPrm")&(1<<13) != 0 || nativeCapabilityBits("CapAmb")&(1<<13) != 0
}

func nativeCapabilityBits(field string) uint64 {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return ^uint64(0)
	}
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, field+":") {
			bits, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, field+":")), 16, 64)
			if err != nil {
				return ^uint64(0)
			}
			return bits
		}
	}
	return ^uint64(0)
}
