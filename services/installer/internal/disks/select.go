package disks

import "sort"

// AutoSelect returns the largest disk, excluding the boot device.
// bootDevice may be empty, in which case no exclusion is applied.
// TODO: boot device detection needs careful implementation — accidentally
// selecting the USB drive the ISO booted from would wipe it mid-install.
func AutoSelect(disks []Disk, bootDevice string) *Disk {
	sorted := sortedBySize(disks)
	for i := range sorted {
		if sorted[i].Device == bootDevice {
			continue
		}
		return &sorted[i]
	}
	return nil
}

// AreAmbiguous returns true when the two largest disks are within 20% of each
// other in size. In that case the UI should show an inline disk picker rather
// than silently auto-selecting one.
func AreAmbiguous(disks []Disk) bool {
	if len(disks) < 2 {
		return false
	}
	sorted := sortedBySize(disks)
	a, b := sorted[0].SizeBytes, sorted[1].SizeBytes
	if a == 0 {
		return false
	}
	diff := float64(a-b) / float64(a)
	return diff < 0.20
}

func sortedBySize(disks []Disk) []Disk {
	out := make([]Disk, len(disks))
	copy(out, disks)
	sort.Slice(out, func(i, j int) bool {
		return out[i].SizeBytes > out[j].SizeBytes
	})
	return out
}
