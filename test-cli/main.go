package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"strings"
)

func main() {
	data, _ := io.ReadAll(os.Stdin)
	text := strings.TrimSpace(string(data))

	if text != "开门" {
		return
	}

	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			r := uint8(50 + x*50/400)
			g := uint8(100 + y*80/200)
			b := uint8(200 - y*50/200)
			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}

	drawBlocks(img, 50, 60, 3, color.RGBA{255, 255, 255, 255})
	drawBlocks(img, 30, 130, 5, color.RGBA{220, 220, 220, 255})

	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, img)

	b64 := base64.StdEncoding.EncodeToString(pngBuf.Bytes())
	fmt.Printf("🚪 门已打开！\n\n![开门](data:image/png;base64,%s)\n", b64)
}

func drawBlocks(img *image.RGBA, startX, startY, count int, c color.RGBA) {
	const w, h = 36, 40
	for i := 0; i < count; i++ {
		x := startX + i*(w+12)
		y := startY
		for dy := 0; dy < h; dy++ {
			for dx := 0; dx < w; dx++ {
				if dy < 2 || dy >= h-2 || dx < 2 || dx >= w-2 {
					img.Set(x+dx, y+dy, color.RGBA{c.R / 2, c.G / 2, c.B / 2, 255})
				} else {
					img.Set(x+dx, y+dy, c)
				}
			}
		}
	}
}
