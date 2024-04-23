package devices

import (
	"image"

	streamdeck "github.com/SKAARHOJ/go-streamdeck"
)

var (
	neoName                     string
	neoButtonWidth              uint
	neoButtonHeight             uint
	neoImageReportPayloadLength uint
)

// GetImageHeaderNeo returns the USB comms header for a button image for the XL
func GetImageHeaderNeo(bytesRemaining uint, btnIndex uint, pageNumber uint) []byte {
	thisLength := uint(0)
	if neoImageReportPayloadLength < bytesRemaining {
		thisLength = neoImageReportPayloadLength
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

func GetImageAreaHeaderNeo(bytesRemaining uint, x, y, width, height uint, pageIndex uint) []byte {
	thisLength := uint(neoImageReportPayloadLength)
	if neoImageReportPayloadLength > bytesRemaining {
		thisLength = bytesRemaining
	}

	header := []byte{'\x02', '\x0b'}

	// 2-3 = 1= last, 0= before that
	if thisLength == bytesRemaining {
		header = append(header, '\x00', '\x01')
	} else {
		header = append(header, '\x00', '\x00')
	}
	// 4-5 = (Little Endian): length (1024-8 = 1016 for full packages)
	header = append(header, byte(thisLength&0xff), byte(thisLength>>8))

	header = append(header, byte(pageIndex&0xff))
	header = append(header, byte(pageIndex>>8))

	return header
}

func init() {
	neoName = "Streamdeck Neo"
	neoButtonWidth = 96
	neoButtonHeight = 96 // Button index 8+9 (paging buttons) are probably about 16 pixels high. At least if you send a 96x96 image to them, only the lower 16 pixels or so will effectively paint the button. It's not completely understood honestly since there is a diffuser in front of it and I have not opened the Stream Deck to check how it really works to the edges (KS). For now I will not care and just generate a solid color 96x96 image to them as a way to set their color. But this could be optimized.
	neoImageReportPayloadLength = 1024
	streamdeck.RegisterDevicetype(
		neoName, // Name
		image.Point{X: int(neoButtonWidth), Y: int(neoButtonHeight)}, // Width/height of a button
		0x9a,                        // USB productID
		resetPacket32(),             // Reset packet
		10,                          // Number of buttons
		2,                           // Number of rows
		4,                           // Number of columns
		brightnessPacket32(),        // Set brightness packet preamble
		4,                           // Button read offset
		"JPEG",                      // Image format
		neoImageReportPayloadLength, // Amount of image payload allowed per USB packet
		nil,
		GetImageHeaderNeo, // Function to get the comms image header
		GetImageAreaHeaderNeo,
	)
}
