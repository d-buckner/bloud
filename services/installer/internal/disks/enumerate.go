package disks

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

type Disk struct {
	Device      string
	SizeBytes   int64
	Model       string
	IsRemovable bool
}

type lsblkOutput struct {
	Blockdevices []lsblkDevice `json:"blockdevices"`
}

type lsblkDevice struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Model string `json:"model"`
	Type  string `json:"type"`
	Tran  string `json:"tran"`
}

func Enumerate() ([]Disk, error) {
	out, err := exec.Command("lsblk", "-J", "-b", "-d", "-o", "NAME,SIZE,MODEL,TYPE,TRAN").Output()
	if err != nil {
		return nil, fmt.Errorf("lsblk failed: %w", err)
	}

	var raw lsblkOutput
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parsing lsblk output: %w", err)
	}

	var disks []Disk
	for _, dev := range raw.Blockdevices {
		if dev.Type != "disk" {
			continue
		}
		disks = append(disks, Disk{
			Device:      "/dev/" + dev.Name,
			SizeBytes:   dev.Size,
			Model:       dev.Model,
			IsRemovable: dev.Tran == "usb",
		})
	}
	return disks, nil
}
