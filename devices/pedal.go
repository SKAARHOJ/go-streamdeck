package devices

import (
	"image"

	streamdeck "github.com/magicmonkey/go-streamdeck"
)

var (
	pedalName                     string
	pedalButtonWidth              uint
	pedalButtonHeight             uint
	pedalImageReportPayloadLength uint
)

func GetImageHeaderPedal(bytesRemaining uint, btnIndex uint, pageNumber uint) []byte {
	return []byte{}
}

func init() {
	pedalName = "Streamdeck Pedal"
	streamdeck.RegisterDevicetype(
		pedalName, // Name
		image.Point{},
		0x86,                 // USB productID
		resetPacket32(),      // Reset packet
		3,                    // Number of buttons
		1,                    // Number of rows
		3,                    // Number of columns
		brightnessPacket32(), // Set brightness packet preamble
		4,                    // Button read offset
		"",                   // Image format
		0,                    // Amount of image payload allowed per USB packet
		nil,
		GetImageHeaderPedal, // Function to get the comms image header
		nil,
	)
}
