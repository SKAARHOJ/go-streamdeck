package devices

import (
	"image"

	streamdeck "github.com/magicmonkey/go-streamdeck"
)

var (
	mk2Name                     string
	mk2ButtonWidth              uint
	mk2ButtonHeight             uint
	mk2ImageReportPayloadLength uint
)

// GetImageHeaderMk2 returns the USB comms header for a button image for the XL
func GetImageHeaderMk2(bytesRemaining uint, btnIndex uint, pageNumber uint) []byte {
	thisLength := uint(0)
	if mk2ImageReportPayloadLength < bytesRemaining {
		thisLength = mk2ImageReportPayloadLength
	} else {
		thisLength = bytesRemaining
	}
	header := []byte{'\x02', '\x07', byte(btnIndex)}
	if thisLength == bytesRemaining {
		header = append(header, '\x01')
	} else {
		header = append(header, '\x00')
	}

	header = append(header, byte(thisLength&0xff))
	header = append(header, byte(thisLength>>8))

	header = append(header, byte(pageNumber&0xff))
	header = append(header, byte(pageNumber>>8))

	return header
}

func init() {
	mk2Name = "Streamdeck MK2"
	mk2ButtonWidth = 72
	mk2ButtonHeight = 72
	mk2ImageReportPayloadLength = 1024
	streamdeck.RegisterDevicetype(
		mk2Name, // Name
		image.Point{X: int(mk2ButtonWidth), Y: int(mk2ButtonHeight)}, // Width/height of a button
		0x80,                        // USB productID
		resetPacket32(),             // Reset packet
		15,                          // Number of buttons
		3,                           // Number of rows
		5,                           // Number of columns
		brightnessPacket32(),        // Set brightness packet preamble
		4,                           // Button read offset
		"JPEG",                      // Image format
		mk2ImageReportPayloadLength, // Amount of image payload allowed per USB packet
		GetImageHeaderMk2,           // Function to get the comms image header
	)
}
