package devices

import (
	"image"

	streamdeck "github.com/SKAARHOJ/go-streamdeck"
)

// Stream Deck + XL (20GBD9901): 36 LCD keys (9 x 4), 6 encoders with push
// and a 1200 x 100 touch strip.
//
// Written from Elgato's HID documentation
// (https://docs.elgato.com/streamdeck/hid/stream-deck-plus-xl) without
// access to a device - verify on hardware. The protocol matches the
// Stream Deck +, except that images are rotated 90 degrees counter-clockwise.

var (
	plusXLName                     string
	plusXLButtonWidth              uint
	plusXLButtonHeight             uint
	plusXLImageReportPayloadLength uint
)

// GetImageHeaderPlusXL returns the USB comms header for a button image
// (command 0x07: target, done flag, chunk size, chunk index).
func GetImageHeaderPlusXL(bytesRemaining uint, btnIndex uint, pageNumber uint) []byte {
	payload := plusXLImageReportPayloadLength - 8
	thisLength := bytesRemaining
	if payload < bytesRemaining {
		thisLength = payload
	}
	header := []byte{'\x02', '\x07', byte(btnIndex)}
	if thisLength == bytesRemaining {
		header = append(header, '\x01')
	} else {
		header = append(header, '\x00')
	}
	header = append(header, byte(thisLength&0xff), byte(thisLength>>8))
	header = append(header, byte(pageNumber&0xff), byte(pageNumber>>8))
	return header
}

// GetImageAreaHeaderPlusXL returns the USB comms header for a partial
// touch strip image (command 0x0C). Coordinates and size are logical
// (unrotated), even though the image data itself is rotated.
func GetImageAreaHeaderPlusXL(bytesRemaining uint, x, y, width, height uint, pageIndex uint) []byte {
	payload := plusXLImageReportPayloadLength - 16
	thisLength := bytesRemaining
	if payload < bytesRemaining {
		thisLength = payload
	}
	header := []byte{'\x02', '\x0c'}
	header = append(header, byte(x&0xff), byte(x>>8))           // +0x02 X
	header = append(header, byte(y&0xff), byte(y>>8))           // +0x04 Y
	header = append(header, byte(width&0xff), byte(width>>8))   // +0x06 width
	header = append(header, byte(height&0xff), byte(height>>8)) // +0x08 height
	if thisLength == bytesRemaining {                           // +0x0A done flag
		header = append(header, '\x01')
	} else {
		header = append(header, '\x00')
	}
	header = append(header, byte(pageIndex&0xff), byte(pageIndex>>8))   // +0x0B chunk index
	header = append(header, byte(thisLength&0xff), byte(thisLength>>8)) // +0x0D chunk size
	header = append(header, byte(0))                                    // +0x0F reserved
	return header
}

func init() {
	plusXLName = "Streamdeck Plus XL"
	plusXLButtonWidth = 112
	plusXLButtonHeight = 112
	plusXLImageReportPayloadLength = 1024
	streamdeck.RegisterDevicetype(
		plusXLName, // Name
		image.Point{X: int(plusXLButtonWidth), Y: int(plusXLButtonHeight)}, // Width/height of a button
		0xC6,                           // USB productID
		resetPacket32(),                // Reset packet
		36,                             // Number of buttons
		4,                              // Number of rows
		9,                              // Number of columns
		brightnessPacket32(),           // Set brightness packet preamble
		4,                              // Button read offset
		"JPEG",                         // Image format
		plusXLImageReportPayloadLength, // Amount of image payload allowed per USB packet
		nil,
		GetImageHeaderPlusXL, // Function to get the comms image header
		GetImageAreaHeaderPlusXL,
	)
}
