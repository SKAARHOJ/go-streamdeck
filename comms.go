package streamdeck

import (
	"errors"
	"image"
	"image/color"

	"github.com/karalabe/hid"
)

const vendorID = 0x0fd9

// deviceType represents one of the various types of StreamDeck (mini/orig/orig2/xl)
type deviceType struct {
	name            string
	imageSize       image.Point
	usbProductID    uint16
	resetPacket     []byte
	numberOfButtons uint
}

var deviceTypes []deviceType

// RegisterDevicetype allows the declaration of a new type of device, intended for use by subpackage "devices"
func RegisterDevicetype(
	name string,
	imageSize image.Point,
	usbProductID uint16,
	resetPacket []byte,
	numberOfButtons uint,
) {
	d := deviceType{
		name:            name,
		imageSize:       imageSize,
		usbProductID:    usbProductID,
		resetPacket:     resetPacket,
		numberOfButtons: numberOfButtons,
	}
	deviceTypes = append(deviceTypes, d)
}

// Device is a struct which represents an actual Streamdeck device, and holds its reference to the USB HID device
type Device struct {
	fd                   *hid.Device
	deviceType           deviceType
	buttonPressListeners []func(int, *Device, error)
}

// Open a Streamdeck device, the most common entry point
func Open() (*Device, error) {
	return rawOpen(true)
}

// OpenWithoutReset will open a Streamdeck device, without resetting it
func OpenWithoutReset() (*Device, error) {
	return rawOpen(false)
}

// Opens a new StreamdeckXL device, and returns a handle
func rawOpen(reset bool) (*Device, error) {
	devices := hid.Enumerate(vendorID, 0)
	if len(devices) == 0 {
		return nil, errors.New("No elgato devices found")
	}

	retval := &Device{}
	id := 0
	// Iterate over the known device types, matching to product ID
	for _, devType := range deviceTypes {
		if devices[id].ProductID == devType.usbProductID {
			retval.deviceType = devType
		}
	}
	dev, err := devices[id].Open()
	if err != nil {
		return nil, err
	}
	retval.fd = dev
	if reset {
		retval.ResetComms()
	}
	go retval.buttonPressListener()
	return retval, nil
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

	payload := []byte{'\x03', '\x08', byte(pct)}
	d.fd.SendFeatureReport(payload)
}

// ClearButtons writes a black square to all buttons
func (d *Device) ClearButtons() {
	for i := 0; i < 32; i++ {
		d.WriteColorToButton(i, color.Black)
	}
}

// WriteColorToButton writes a specified color to the given button
func (d *Device) WriteColorToButton(btnIndex int, colour color.Color) {
	img := getSolidColourImage(colour)
	d.rawWriteToButton(btnIndex, getImageAsJpeg(img))
}

// WriteImageToButton writes a specified image file to the given button
func (d *Device) WriteImageToButton(btnIndex int, filename string) error {
	img, err := getImageFile(filename)
	if err != nil {
		return err
	}
	d.WriteRawImageToButton(btnIndex, img)
	return nil
}

func (d *Device) buttonPressListener() {
	var buttonMask [32]bool
	for {
		data := make([]byte, 50)
		_, err := d.fd.Read(data)
		if err != nil {
			d.sendButtonPressEvent(-1, err)
			break
		}
		for i := 0; i < 32; i++ {
			if data[4+i] == 1 {
				if !buttonMask[i] {
					d.sendButtonPressEvent(i, nil)
				}
				buttonMask[i] = true
			} else {
				buttonMask[i] = false
			}
		}
	}
}

func (d *Device) sendButtonPressEvent(btnIndex int, err error) {
	for _, f := range d.buttonPressListeners {
		f(btnIndex, d, err)
	}
}

// ButtonPress registers a callback to be called whenever a button is pressed
func (d *Device) ButtonPress(f func(int, *Device, error)) {
	d.buttonPressListeners = append(d.buttonPressListeners, f)
}

// ResetComms will reset the comms protocol to the StreamDeck; useful if things have gotten de-synced, but it will also reboot the StreamDeck
func (d *Device) ResetComms() {
	payload := []byte{'\x03', '\x02'}
	d.fd.SendFeatureReport(payload)
}

// WriteRawImageToButton takes an `image.Image` and writes it to the given button, after resizing and rotating the image to fit the button (for some reason the StreamDeck screens are all upside down)
func (d *Device) WriteRawImageToButton(btnIndex int, rawImg image.Image) error {
	img := resizeAndRotate(rawImg, 96, 96)
	return d.rawWriteToButton(btnIndex, getImageAsJpeg(img))
}

func (d *Device) rawWriteToButton(btnIndex int, rawImage []byte) error {
	// Based on set_key_image from https://github.com/abcminiuser/python-elgato-streamdeck/blob/master/src/StreamDeck/Devices/StreamDeckXL.py#L151
	pageNumber := 0
	bytesRemaining := len(rawImage)

	imageReportLength := 1024
	imageReportHeaderLength := 8
	imageReportPayloadLength := imageReportLength - imageReportHeaderLength

	// Surely no image can be more than 20 packets...?
	payloads := make([][]byte, 20)

	for bytesRemaining > 0 {
		thisLength := 0
		if imageReportPayloadLength < bytesRemaining {
			thisLength = imageReportPayloadLength
		} else {
			thisLength = bytesRemaining
		}
		bytesSent := pageNumber * imageReportPayloadLength
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

		payload := append(header, rawImage[bytesSent:(bytesSent+thisLength)]...)
		padding := make([]byte, imageReportLength-len(payload))

		thingToSend := append(payload, padding...)
		d.fd.Write(thingToSend)
		payloads[pageNumber] = thingToSend

		bytesRemaining = bytesRemaining - thisLength
		pageNumber = pageNumber + 1
		if pageNumber >= len(payloads) {
			return errors.New("Image too big for button, aborting.  You probably need to reset the Streamdeck at this stage, and modify the size of `payloads` on line 142 to be something bigger.")
		}
	}
	return nil
}
