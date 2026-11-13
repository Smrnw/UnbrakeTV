package tv

import (
	"regexp"
	"strconv"
)

// Storage is what occupies the internal memory, in bytes (from dumpsys diskstats).
type Storage struct {
	Apps      int64 `json:"apps"`
	Data      int64 `json:"data"`
	Cache     int64 `json:"cache"`
	Photos    int64 `json:"photos"`
	Videos    int64 `json:"videos"`
	Audio     int64 `json:"audio"`
	Downloads int64 `json:"downloads"`
	System    int64 `json:"system"`
	Other     int64 `json:"other"`
}

var storageLine = regexp.MustCompile(`^(App|App Data|App Cache|Photos|Videos|Audio|Downloads|System|Other) Size:\s*(\d+)`)

func parseStorage(out string) Storage {
	var s Storage
	for _, l := range lines(out) {
		m := storageLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		v, _ := strconv.ParseInt(m[2], 10, 64)
		switch m[1] {
		case "App":
			s.Apps = v
		case "App Data":
			s.Data = v
		case "App Cache":
			s.Cache = v
		case "Photos":
			s.Photos = v
		case "Videos":
			s.Videos = v
		case "Audio":
			s.Audio = v
		case "Downloads":
			s.Downloads = v
		case "System":
			s.System = v
		case "Other":
			s.Other = v
		}
	}
	return s
}

// Storage returns the breakdown read by the last Packages() call.
func (d *Device) Storage() Storage {
	d.packageSizes()
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.storage
}
