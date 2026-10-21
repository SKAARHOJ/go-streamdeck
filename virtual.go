package streamdeck

import (
	"fmt"
	"sort"
)

// OpenVirtual returns a Device of the given model without any hardware
// behind it. It reports the model's properties like a real device, and all
// output to it is silently ignored. It never produces input events.
// Use it to run a panel before (or without) the physical Stream Deck.
func OpenVirtual(model string, serial string) (*Device, error) {
	for _, devType := range deviceTypes {
		if devType.name == model {
			d := &Device{deviceType: devType}
			d.deviceType.serial = serial
			return d, nil
		}
	}
	return nil, fmt.Errorf("unknown Stream Deck model %q", model)
}

// IsVirtual returns true for devices made with OpenVirtual.
func (d *Device) IsVirtual() bool {
	return d.fd == nil
}

// GetModels returns the names of all registered Stream Deck models.
func GetModels() []string {
	names := []string{}
	for _, devType := range deviceTypes {
		names = append(names, devType.name)
	}
	sort.Strings(names)
	return names
}
