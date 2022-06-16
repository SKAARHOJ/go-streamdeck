package streamdeck

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // Allow gifs to be loaded
	"image/jpeg"
	_ "image/png" // Allow pngs to be loaded
	"os"

	"github.com/disintegration/gift"
	"golang.org/x/image/bmp"
)

func resizeAndRotate(img image.Image, width, height int, devname string) image.Image {
	g, _ := deviceSpecifics(devname, width, height)
	res := image.NewRGBA(g.Bounds(img.Bounds()))
	g.Draw(res, img)
	return res
}

func deviceSpecifics(devName string, width, height int) (*gift.GIFT, error) {
	switch devName {
	case "Streamdeck XL", "Streamdeck (original v2)", "Streamdeck MK2":
		return gift.New(
			gift.Resize(width, height, gift.LanczosResampling),
			gift.Rotate180(),
		), nil
	case "Streamdeck Mini":
		return gift.New(
			gift.Resize(width, height, gift.LanczosResampling),
			gift.Rotate90(),
			gift.FlipVertical(),
		), nil
	case "Streamdeck (original)":
		return gift.New(
			gift.Resize(width, height, gift.LanczosResampling),
			gift.Rotate180(),
		), nil
	default:
		return nil, errors.New(fmt.Sprintf("Unsupported Device: %s", devName))
	}
}

func getImageForButton(img image.Image, btnFormat string) ([]byte, error) {
	var b bytes.Buffer
	switch btnFormat {
	case "JPEG":
		jpeg.Encode(&b, img, &jpeg.Options{Quality: 100})
	case "BMP":
		// Opaque images are necessary, otherwise you won't get anything but black. So, lets remove any alpha channel without regard to pre-multiplied alpha (could be done smarter, but we assume there is no significant transparency in the image in the first place)
		if !img.(*image.RGBA).Opaque() {
			bounds := img.(*image.RGBA).Bounds()
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
					r, g, b, _ := img.(*image.RGBA).At(x, y).RGBA()
					img.(*image.RGBA).Set(x, y, color.RGBA{uint8(r), uint8(g), uint8(b), 255})
				}
			}
		}

		bmp.Encode(&b, img)
	default:
		return nil, errors.New("Unknown button image format: " + btnFormat)
	}
	return b.Bytes(), nil
}

func getSolidColourImage(colour color.Color, btnSize int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, btnSize, btnSize))
	//colour := color.RGBA{red, green, blue, 0}
	draw.Draw(img, img.Bounds(), image.NewUniform(colour), image.Point{0, 0}, draw.Src)
	return img
}

func getImageFile(filename string) (image.Image, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	return img, nil
}
