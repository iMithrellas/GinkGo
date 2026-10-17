package voice

import (
	"fmt"
	"os"
	"strings"
)

func residentSocketTableOwned(data []byte, local, remote string, pid int) bool {
	var inode string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 10 && fields[1] == local && fields[2] == remote && fields[3] == "01" {
			inode = fields[9]
			break
		}
	}
	if inode == "" || inode == "0" {
		return false
	}
	dir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		target, err := os.Readlink(dir + "/" + entry.Name())
		if err == nil && target == "socket:["+inode+"]" {
			return true
		}
	}
	return false
}
