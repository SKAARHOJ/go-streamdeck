package devices

import (
	"image"

	streamdeck "github.com/SKAARHOJ/go-streamdeck"
)

var (
	plusName                     string
	plusButtonWidth              uint
	plusButtonHeight             uint
	plusImageReportPayloadLength uint
)

// GetImageHeaderPlus returns the USB comms header for a button image for the XL
func GetImageHeaderPlus(bytesRemaining uint, btnIndex uint, pageNumber uint) []byte {
	thisLength := uint(0)
	if plusImageReportPayloadLength < bytesRemaining {
		thisLength = plusImageReportPayloadLength
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

// GetImageAreadHeaderPlus returns the USB comms header for the display area above the encoders
/*

Captured package headers for display on encoder 1,2,3,4:

02 0c 00 00 00 00 c8 00 64 00 00 00 00 f0 03 00 (ff d8 ff e0 00 10 ...)
02 0c 00 00 00 00 c8 00 64 00 00 01 00 f0 03 00 ....
02 0c 00 00 00 00 c8 00 64 00 01 02 00 76 02 00 ....

02 0c c8 00 00 00 c8 00 64 00 00 00 00 f0 03 00 (ff d8 ff e0 00 10 ...)
02 0c c8 00 00 00 c8 00 64 00 00 01 00 f0 03 00 ....
02 0c c8 00 00 00 c8 00 64 00 01 02 00 76 02 00 ....

02 0c 90 01 00 00 c8 00 64 00 00 00 00 f0 03 00 (ff d8 ff e0 00 10 ...)
02 0c 90 01 00 00 c8 00 64 00 00 01 00 f0 03 00 ....
02 0c 90 01 00 00 c8 00 64 00 01 02 00 76 02 00 ....

02 0c 58 02 00 00 c8 00 64 00 00 00 00 f0 03 00 (ff d8 ff e0 00 10 ...)
02 0c 58 02 00 00 c8 00 64 00 00 01 00 f0 03 00 ....
02 0c 58 02 00 00 c8 00 64 00 01 02 00 76 02 00 ....

0-1 = header: 02 0c
2-3 = x-start (Little Endian): 0, 200, 400, 600
4-5 = y-start? 0,0,0,0
6-7 = width (Little Endian): 200
8-9 = height (Little Endian): 100
10 = 1= last, 0= before that
11 = page index
12 = ...
13-14 = (Little Endian): length (1024-16 = 1008 for full packages)
15 = ...

*/
// y is so far unknown - not even if it exists, but overflowing the X-value will bring the rendering to the next line.
// If x+width exceeds 800 pixel width, it will render "on the next line" of the display.
func GetImageAreaHeaderPlus(bytesRemaining uint, x, y, width, height uint, pageIndex uint) []byte {
	thisLength := uint(plusImageReportPayloadLength)
	if plusImageReportPayloadLength > bytesRemaining {
		thisLength = bytesRemaining
	}

	header := []byte{'\x02', '\x0c'}

	// 2-3 = x-start (Little Endian): 0, 200, 400, 600
	header = append(header, byte(x&0xff), byte(x>>8))

	// 4-5 = ?
	header = append(header, 0, 0)

	// 6-7 = width (Little Endian)
	header = append(header, byte(width&0xff), byte(width>>8))

	// 8-9 = height (Little Endian)
	header = append(header, byte(height&0xff), byte(height>>8))

	// 10 = 1= last, 0= before that
	if thisLength == bytesRemaining {
		header = append(header, '\x01')
	} else {
		header = append(header, '\x00')
	}

	// 11 = page index
	// 12 = ...
	header = append(header, byte(pageIndex&0xff), byte(pageIndex>>8))

	// 13-14 = (Little Endian): length (1024-16 = 1008 for full packages)
	header = append(header, byte(thisLength&0xff), byte(thisLength>>8))

	// Padding up to 16 bytes:
	header = append(header, byte(0))

	//fmt.Println(header)
	return header
}

func init() {
	plusName = "Streamdeck Plus"
	plusButtonWidth = 120
	plusButtonHeight = 120
	plusImageReportPayloadLength = 1024
	streamdeck.RegisterDevicetype(
		plusName, // Name
		image.Point{X: int(plusButtonWidth), Y: int(plusButtonHeight)}, // Width/height of a button
		0x84,                         // USB productID
		resetPacket32(),              // Reset packet
		8,                            // Number of buttons
		2,                            // Number of rows
		4,                            // Number of columns
		brightnessPacket32(),         // Set brightness packet preamble
		4,                            // Button read offset
		"JPEG",                       // Image format
		plusImageReportPayloadLength, // Amount of image payload allowed per USB packet
		nil,
		GetImageHeaderPlus, // Function to get the comms image header
		GetImageAreaHeaderPlus,
	)
}
