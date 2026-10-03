package storageguard

import "syscall"

// DiskUsage returns the free and total byte counts of the filesystem holding
// path. It reuses the guard's statfs math so callers (e.g. the torrent
// manager's download admission control) judge against the exact numbers the
// watermarks use. ok is false when the path cannot be statfs'ed.
func DiskUsage(path string) (free, total uint64, ok bool) {
	u := getDiskUsage(path)
	return u.FreeBytes, u.TotalBytes, u.Valid
}

// diskUsage is a snapshot of the staging filesystem capacity numbers.
type diskUsage struct {
	TotalBytes  uint64
	FreeBytes   uint64
	UsedBytes   uint64
	UsedPercent float64
	Valid       bool
}

// getDiskUsage measures the filesystem holding the torrent staging area
// (same statfs math as the Files handler so the admin sees consistent numbers).
func getDiskUsage(path string) diskUsage {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return diskUsage{}
	}
	bs := uint64(st.Bsize)
	total := st.Blocks * bs
	free := st.Bfree * bs
	used := total - free
	pct := 0.0
	if total > 0 {
		pct = float64(used) / float64(total) * 100.0
	}
	return diskUsage{TotalBytes: total, FreeBytes: free, UsedBytes: used, UsedPercent: pct, Valid: true}
}
