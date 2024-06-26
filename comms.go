package streamdeck

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"time"

	"github.com/disintegration/gift"
	"github.com/karalabe/hid"
	log "github.com/s00500/env_logger"
)

const vendorID = 0x0fd9

type deviceSearchResult struct {
	Name      string
	Serial    string
	ProductID uint16
}

// deviceType represents one of the various types of StreamDeck (mini/orig/orig2/xl)
type deviceType struct {
	name                string
	imageSize           image.Point
	usbProductID        uint16
	resetPacket         []byte
	numberOfButtons     uint
	buttonRows          uint
	buttonCols          uint
	brightnessPacket    []byte
	buttonReadOffset    uint
	imageFormat         string
	imagePayloadPerPage uint
	imageHeaderFunc     func(bytesRemaining uint, btnIndex uint, pageNumber uint) []byte
	imageAreaHeaderFunc func(bytesRemaining uint, x, y, width, height uint, pageNumber uint) []byte
	serial              string
	buttonMap           map[uint]int
}

var deviceTypes []deviceType

// RegisterDevicetype allows the declaration of a new type of device, intended for use by subpackage "devices"
func RegisterDevicetype(
	name string,
	imageSize image.Point,
	usbProductID uint16,
	resetPacket []byte,
	numberOfButtons uint,
	buttonRows uint,
	buttonCols uint,
	brightnessPacket []byte,
	buttonReadOffset uint,
	imageFormat string,
	imagePayloadPerPage uint,
	buttonMap map[uint]int,
	imageHeaderFunc func(bytesRemaining uint, btnIndex uint, pageNumber uint) []byte,
	imageAreaHeaderFunc func(bytesRemaining uint, x, y, width, height uint, pageNumber uint) []byte,
) {
	d := deviceType{
		name:                name,
		imageSize:           imageSize,
		usbProductID:        usbProductID,
		resetPacket:         resetPacket,
		numberOfButtons:     numberOfButtons,
		buttonRows:          buttonRows,
		buttonCols:          buttonCols,
		brightnessPacket:    brightnessPacket,
		buttonReadOffset:    buttonReadOffset,
		imageFormat:         imageFormat,
		imagePayloadPerPage: imagePayloadPerPage,
		buttonMap:           buttonMap,
		imageHeaderFunc:     imageHeaderFunc,
		imageAreaHeaderFunc: imageAreaHeaderFunc,
	}
	deviceTypes = append(deviceTypes, d)
}

// Device is a struct which represents an actual Streamdeck device, and holds its reference to the USB HID device
type Device struct {
	fd         *hid.Device
	deviceType deviceType

	buttonPressListeners     []func(int, *Device, error, bool)
	encoderPushListeners     []func(int, *Device, bool)
	encoderRotationListeners []func(int, *Device, int)
	touchPushListeners       []func(*Device, uint16, uint16, bool)
	touchSwipeListeners      []func(*Device, uint16, uint16, uint16, uint16)
}

// Open a Streamdeck device, the most common entry point
func Open() (*Device, error) {
	return rawOpen(true, "")
}

// Open a Streamdeck device, the most common entry point
func OpenBySerial(serial string) (*Device, error) {
	return rawOpen(true, serial)
}

// Search for streamdeck devices
func Search() []*deviceSearchResult {
	result := []*deviceSearchResult{}
	devices := hid.Enumerate(vendorID, 0)
	for _, device := range devices {
		result = append(result, &deviceSearchResult{
			ProductID: device.ProductID,
			Serial:    device.Serial,
			Name:      device.Product,
		})
	}
	return result
}

// OpenWithoutReset will open a Streamdeck device, without resetting it
func OpenWithoutReset() (*Device, error) {
	return rawOpen(false, "")
}

// Opens a new StreamdeckXL device, and returns a handle
func rawOpen(reset bool, serial string) (*Device, error) {
	devices := hid.Enumerate(vendorID, 0)
	if len(devices) == 0 {
		return nil, errors.New("No elgato devices found")
	}

	retval := &Device{}
	for _, device := range devices {
		// Iterate over the known device types, matching to product ID
		log.Debugln(log.Indent(device))
		for _, devType := range deviceTypes {
			if device.ProductID == devType.usbProductID {
				if serial == "" || serial == device.Serial {
					retval.deviceType = devType
					retval.deviceType.serial = device.Serial
					dev, err := device.Open()
					if err != nil {
						return nil, err
					}
					retval.fd = dev
					if reset {
						retval.ResetComms()
					}
					go retval.eventListener()
					return retval, nil
				}
			}
		}
	}
	return nil, errors.New("Found an Elgato device, but not one for which there is a definition; have you imported the devices package?")
}

// GetSerial returns the device serial
func (d *Device) GetSerial() string {
	return d.deviceType.serial
}

// GetName returns the name of the type of Streamdeck
func (d *Device) GetName() string {
	return d.deviceType.name
}

// GetImageSize returns the size of images on this Streamdeck
func (d *Device) GetImageSize() image.Point {
	return d.deviceType.imageSize
}

func (d *Device) HasImageCapability() bool {
	return d.deviceType.imageSize != image.Point{}
}

func (d *Device) GetNumberOfButtons() uint {
	return d.deviceType.numberOfButtons
}

func (d *Device) GetUSBProductId() int {
	return int(d.deviceType.usbProductID)
}

// Close the device
func (d *Device) Close() {
	d.fd.Close()
}

// SetBrightness sets the button brightness
// pct is an integer between 0-100
func (d *Device) SetBrightness(pct int) {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}

	preamble := d.deviceType.brightnessPacket
	payload := append(preamble, byte(pct))
	d.fd.SendFeatureReport(payload)
}

// ClearButtons writes a black square to all buttons
func (d *Device) ClearButtons() {
	numButtons := int(d.deviceType.numberOfButtons)
	for i := 0; i < numButtons; i++ {
		d.WriteColorToButton(i, color.Black)
	}
}

// WriteColorToButton writes a specified color to the given button
func (d *Device) WriteColorToButton(btnIndex int, colour color.Color) error {
	btnIndex = int(d.mapButtonIn(uint(btnIndex)))
	if !d.HasImageCapability() {
		return errors.New("Button doesn't have image capability")
	}

	img := getSolidColourImage(colour, d.deviceType.imageSize.X)
	imgForButton, err := getImageForButton(img, d.deviceType.imageFormat)
	if err != nil {
		return err
	}
	return d.rawWriteToButton(btnIndex, imgForButton)
}

// WriteImageToButton writes a specified image file to the given button
func (d *Device) WriteImageToButton(btnIndex int, filename string) error {
	//btnIndex = int(d.mapButtonIn(uint(btnIndex)))
	if !d.HasImageCapability() {
		return errors.New("Button doesn't have image capability")
	}

	img, err := getImageFile(filename)
	if err != nil {
		return err
	}
	d.WriteRawImageToButton(btnIndex, img)
	return nil
}

func (d *Device) eventListener() {
	var buttonMask []bool
	buttonMask = make([]bool, d.deviceType.numberOfButtons)

	buttonTime := make([]time.Time, d.deviceType.numberOfButtons)
	for i := range buttonTime {
		buttonTime[i] = time.Now()
	}

	// All for Streamdeck Plus:
	numberOfEncoders := 4
	encoderReadOffset := 5
	encoderButtonTime := make([]time.Time, numberOfEncoders)
	for i := range encoderButtonTime {
		encoderButtonTime[i] = time.Now()
	}
	var encoderButtonMask []bool
	encoderButtonMask = make([]bool, numberOfEncoders)

	for {
		data := make([]byte, 255) // d.deviceType.numberOfButtons+d.deviceType.buttonReadOffset
		_, err := d.fd.Read(data)
		if err != nil {
			d.sendButtonPressEvent(-1, err)
			break
		}

		if data[0] == 1 { // Seems like the first byte is always one for events...
			if d.deviceType.name == "Streamdeck Plus" && data[1] > 0 {
				switch data[1] {
				case 2: // Touch
					switch data[4] {
					case 1: // Tap
						xpos := binary.LittleEndian.Uint16(data[6:])
						ypos := binary.LittleEndian.Uint16(data[8:])
						d.sendTouchPushEvent(xpos, ypos, false)
					case 2: // Press/Hold
						xpos := binary.LittleEndian.Uint16(data[6:])
						ypos := binary.LittleEndian.Uint16(data[8:])
						d.sendTouchPushEvent(xpos, ypos, true)
					case 3: // Swipe
						xstart := binary.LittleEndian.Uint16(data[6:])
						ystart := binary.LittleEndian.Uint16(data[8:])
						xstop := binary.LittleEndian.Uint16(data[10:])
						ystop := binary.LittleEndian.Uint16(data[12:])
						/*						fmt.Printf("SWIPE: xstart=%d, ystart=%d, xstop=%d, ystop=%d; %s,%s\n", xstart, ystart, xstop, ystop,
												su.Qstr((int(xstop)-int(xstart)) > 0, "Right", "Left"), su.Qstr((int(ystop)-int(ystart)) < 0, "Up", "Down"))
						*/
						d.sendTouchSwipeEvent(xstart, ystart, xstop, ystop)
					}
				case 3: // Encoders
					switch data[4] {
					case 1: // Rotate
						for i := 0; i < numberOfEncoders; i++ {
							if data[encoderReadOffset+i] > 0 {
								rev := int(data[encoderReadOffset+i])
								if rev > 127 {
									rev = rev - 256
								}
								d.sendEncoderRotateEvent(i, rev)
							}
						}
					case 0: // Press
						for i := 0; i < numberOfEncoders; i++ {
							if data[encoderReadOffset+i] == 1 {
								if time.Now().After(encoderButtonTime[i].Add(time.Duration(time.Millisecond * 100))) { // Implement 100 ms debouncing on button presses.
									if !encoderButtonMask[i] {
										d.sendEncoderPushEvent(i, true)
										encoderButtonTime[i] = time.Now()
									}
									encoderButtonMask[i] = true
								}
							} else {
								if encoderButtonMask[i] {
									d.sendEncoderPushEvent(i, false)
									encoderButtonMask[i] = false // Putting it here instead of outside the condition because we ONLY want release events if there has been a Press event first (related to the fact that debouncing above can lead to ignored events)
								}
							}

						}
					}
				}
			} else {
				// Standard button stuff
				for i := uint(0); i < d.deviceType.numberOfButtons; i++ {
					if data[d.deviceType.buttonReadOffset+i] == 1 {
						if time.Now().After(buttonTime[i].Add(time.Duration(time.Millisecond * 100))) { // Implement 100 ms debouncing on button presses.
							if !buttonMask[i] {
								d.sendButtonPressEvent(d.mapButtonOut(i), nil)
								buttonTime[i] = time.Now()
							}
							buttonMask[i] = true
						}
					} else {
						if buttonMask[i] {
							d.sendButtonReleaseEvent(d.mapButtonOut(i), nil)
							buttonMask[i] = false // Putting it here instead of outside the condition because we ONLY want release events if there has been a Press event first (related to the fact that debouncing above can lead to ignored events)
						}
					}
				}
			}
		}
	}
}

func (d *Device) mapButtonOut(btnIndex uint) int {
	if d.deviceType.buttonMap != nil {
		if _, exists := d.deviceType.buttonMap[btnIndex]; exists {
			btnIndex = uint(d.deviceType.buttonMap[btnIndex])
		}
	}

	return int(btnIndex)
}

func (d *Device) mapButtonIn(btnIndex uint) int {
	if d.deviceType.buttonMap != nil {
		for out, match := range d.deviceType.buttonMap {
			if uint(match) == btnIndex {
				return int(out)
			}
		}
	}

	return int(btnIndex)
}

func (d *Device) sendButtonPressEvent(btnIndex int, err error) {
	for _, f := range d.buttonPressListeners {
		f(btnIndex, d, err, true)
	}
}

func (d *Device) sendButtonReleaseEvent(btnIndex int, err error) {
	for _, f := range d.buttonPressListeners {
		f(btnIndex, d, err, false)
	}
}

func (d *Device) sendEncoderPushEvent(btnIndex int, pressed bool) {
	for _, f := range d.encoderPushListeners {
		f(btnIndex, d, pressed)
	}
}

func (d *Device) sendEncoderRotateEvent(btnIndex int, pulses int) {
	for _, f := range d.encoderRotationListeners {
		f(btnIndex, d, pulses)
	}
}

func (d *Device) sendTouchPushEvent(xpos, ypos uint16, hold bool) {
	for _, f := range d.touchPushListeners {
		f(d, xpos, ypos, hold)
	}
}

func (d *Device) sendTouchSwipeEvent(xstart, ystart, xstop, ystop uint16) {
	for _, f := range d.touchSwipeListeners {
		f(d, xstart, ystart, xstop, ystop)
	}
}

// ButtonPress registers a callback to be called whenever a button is pressed (or connection is lost!)
func (d *Device) ButtonPress(f func(int, *Device, error, bool)) {
	d.buttonPressListeners = append(d.buttonPressListeners, f)
}

// EncoderPress registers a callback to be called whenever an encoder is pressed
func (d *Device) EncoderPress(f func(int, *Device, bool)) {
	d.encoderPushListeners = append(d.encoderPushListeners, f)
}

// EncoderRotate registers a callback to be called whenever an encoder is rotated
func (d *Device) EncoderRotate(f func(int, *Device, int)) {
	d.encoderRotationListeners = append(d.encoderRotationListeners, f)
}

// TouchPush registers a callback to be called whenever the touch area is pushed (tap or hold)
func (d *Device) TouchPush(f func(*Device, uint16, uint16, bool)) {
	d.touchPushListeners = append(d.touchPushListeners, f)
}

// TouchSwipe registers a callback to be called whenever the touch area is swiped
func (d *Device) TouchSwipe(f func(*Device, uint16, uint16, uint16, uint16)) {
	d.touchSwipeListeners = append(d.touchSwipeListeners, f)
}

// ResetComms will reset the comms protocol to the StreamDeck; useful if things have gotten de-synced, but it will also reboot the StreamDeck
func (d *Device) ResetComms() {
	payload := d.deviceType.resetPacket
	d.fd.SendFeatureReport(payload)
}

// WriteRawImageToButton takes an `image.Image` and writes it to the given button, after resizing and rotating the image to fit the button (for some reason the StreamDeck screens are all upside down)
func (d *Device) WriteRawImageToButton(btnIndex int, rawImg image.Image) error {
	btnIndex = int(d.mapButtonIn(uint(btnIndex)))
	if !d.HasImageCapability() {
		return errors.New("Button doesn't have image capability")
	}
	img := resizeAndRotate(rawImg, d.deviceType.imageSize.X, d.deviceType.imageSize.Y, d.deviceType.name)
	imgForButton, err := getImageForButton(img, d.deviceType.imageFormat)
	if err != nil {
		return err
	}
	return d.rawWriteToButton(btnIndex, imgForButton)
}

func (d *Device) rawWriteToButton(btnIndex int, rawImage []byte) error {
	// Based on set_key_image from https://github.com/abcminiuser/python-elgato-streamdeck/blob/master/src/StreamDeck/Devices/StreamDeckXL.py#L151

	if Min(Max(btnIndex, 0), int(d.deviceType.numberOfButtons)) != btnIndex {
		return errors.New(fmt.Sprintf("Invalid key index: %d", btnIndex))
	}

	pageNumber := 0
	bytesRemaining := len(rawImage)
	halfImage := len(rawImage) / 2
	bytesSent := 0

	for bytesRemaining > 0 {

		header := d.deviceType.imageHeaderFunc(uint(bytesRemaining), uint(btnIndex), uint(pageNumber))
		imageReportLength := int(d.deviceType.imagePayloadPerPage)
		imageReportHeaderLength := len(header)
		imageReportPayloadLength := imageReportLength - imageReportHeaderLength

		if halfImage > imageReportPayloadLength {
			//			log.Fatalf("image too large: %d", halfImage*2)
		}

		thisLength := 0
		if imageReportPayloadLength < bytesRemaining {
			if d.deviceType.name == "Streamdeck (original)" {
				thisLength = halfImage
			} else {
				thisLength = imageReportPayloadLength
			}
		} else {
			thisLength = bytesRemaining
		}

		payload := append(header, rawImage[bytesSent:(bytesSent+thisLength)]...)
		padding := make([]byte, imageReportLength-len(payload))

		thingToSend := append(payload, padding...)
		d.fd.Write(thingToSend)

		bytesRemaining = bytesRemaining - thisLength
		pageNumber = pageNumber + 1
		bytesSent = bytesSent + thisLength
	}
	return nil
}

// y doesn't work, keep it zero!
func (d *Device) WriteRawImageToAreaUnscaled(x, y int, rawImg image.Image) error {
	img := rawImg
	if d.GetName() == "Streamdeck Neo" { // Rotate Info Display for Streamdeck Neo
		g := gift.New(gift.Rotate180())
		newimg := image.NewRGBA(g.Bounds(rawImg.Bounds()))
		g.Draw(newimg, rawImg)
		img = newimg
	}

	imgForButton, err := getImageForButton(img, d.deviceType.imageFormat)
	if err != nil {
		return err
	}

	return d.rawWriteToArea(x, y, rawImg.Bounds().Max.X, rawImg.Bounds().Max.Y, imgForButton)
}

// y doesn't work, keep it zero!
func (d *Device) rawWriteToArea(x, y, width, height int, rawImage []byte) error {
	pageNumber := 0
	bytesRemaining := len(rawImage)
	bytesSent := 0

	for bytesRemaining > 0 {

		header := d.deviceType.imageAreaHeaderFunc(uint(bytesRemaining), uint(x), uint(y), uint(width), uint(height), uint(pageNumber))
		imageReportLength := int(d.deviceType.imagePayloadPerPage)
		imageReportHeaderLength := len(header)
		imageReportPayloadLength := imageReportLength - imageReportHeaderLength

		thisLength := imageReportPayloadLength
		if imageReportPayloadLength > bytesRemaining {
			thisLength = bytesRemaining
		}

		payload := append(header, rawImage[bytesSent:(bytesSent+thisLength)]...)
		padding := make([]byte, imageReportLength-len(payload))

		thingToSend := append(payload, padding...)
		d.fd.Write(thingToSend)

		bytesRemaining = bytesRemaining - thisLength
		pageNumber = pageNumber + 1
		bytesSent = bytesSent + thisLength
	}
	return nil
}

// Golang Min/Max
func Min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

func Max(x, y int) int {
	if x > y {
		return x
	}
	return y
}
