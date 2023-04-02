package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/exp/slog"
)

func main() {
	upstream := os.Getenv("upstream_base")
	if len(upstream) == 0 {
		slog.Error("ntppool upstream_base not set")
		time.Sleep(1 * time.Second)
		os.Exit(2)
	}

	ctx, cancel := chromedp.NewContext(
		context.Background(),
		// chromedp.WithDebugf(log.Printf),
	)
	defer cancel()

	if err := chromedp.Run(ctx); err != nil {
		panic(err)
	}

	e := echo.New()
	e.Add("GET", "/image/offset/:ip", offsetHandler(ctx, upstream), middleware.Logger())
	e.Add("GET", "/__health", func(c echo.Context) error {
		// todo: check that the browser actually works
		return c.String(200, "ok")
	})
	e.Start(":8000")

	// buf, err := takeScreenshot(ctx, "17.253.2.251")
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// if err := os.WriteFile("offset.png", buf, 0o644); err != nil {
	// 	log.Fatal(err)
	// }

}

func offsetHandler(mainCtx context.Context, upstream string) func(echo.Context) error {
	return func(c echo.Context) error {
		ipStr := c.Param("ip")
		slog.Info("offsetHandler", "ip", ipStr)

		buf, err := takeScreenshot(mainCtx, upstream, ipStr)
		if err != nil {
			return c.String(500, err.Error())
		}

		return c.Blob(200, "image/png", buf)
	}
}

func takeScreenshot(mainCtx context.Context, upstream, ip string) ([]byte, error) {

	ctx, _ := chromedp.NewContext(mainCtx)

	url := fmt.Sprintf("%s/scores/%s?graph_only=1", upstream, ip)

	// capture screenshot of an element
	var buf []byte
	if err := chromedp.Run(ctx, elementScreenshot(url, `#graph`, &buf)); err != nil {
		log.Fatal(err)
	}

	// if err := chromedp.Run(ctx, fullScreenshot(url, 90, &buf)); err != nil {
	// 	log.Fatal(err)
	// }

	// // capture entire browser viewport, returning png with quality=90
	// if err := chromedp.Run(ctx, fullScreenshot(url, 90, &buf)); err != nil {
	// 	log.Fatal(err)
	// }
	// if err := os.WriteFile("fullScreenshot.png", buf, 0o644); err != nil {
	// 	log.Fatal(err)
	// }

	return buf, nil

}

// elementScreenshot takes a screenshot of a specific element.
func elementScreenshot(urlstr, sel string, res *[]byte) chromedp.Tasks {

	viewX := 233
	viewY := 501

	return chromedp.Tasks{
		chromedp.Navigate(urlstr),
		chromedp.EmulateViewport(int64(viewY), int64(viewX)),
		chromedp.WaitReady("#loaded", chromedp.NodeVisible),
		chromedp.Sleep(100 * time.Millisecond),
		chromedp.Screenshot(sel, res, chromedp.NodeVisible),
	}
}

// fullScreenshot takes a screenshot of the entire browser viewport.
//
// Note: chromedp.FullScreenshot overrides the device's emulation settings. Use
// device.Reset to reset the emulation and viewport settings.
// func fullScreenshot(urlstr string, quality int, res *[]byte) chromedp.Tasks {
// 	return chromedp.Tasks{
// 		chromedp.Navigate(urlstr),
// 		chromedp.WaitReady("#loaded", chromedp.NodeVisible),
// 		chromedp.FullScreenshot(res, quality),
// 	}
// }
