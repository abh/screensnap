package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"net/http"
	"net/http/pprof"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"golang.org/x/exp/slog"
)

type httpErr struct {
	status int
}

func (m *httpErr) Error() string {
	return fmt.Sprintf("http status %d", m.status)
}

func main() {
	upstream := os.Getenv("upstream_base")
	if len(upstream) == 0 {
		slog.Error("ntppool upstream_base not set")
		time.Sleep(1 * time.Second)
		os.Exit(2)
	}
	upstream = strings.TrimSuffix(upstream, "/")

	opts := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.DisableGPU,
	)
	// create chromedp's context
	parentCtx, execCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer execCancel()

	// create a new browser
	browserCtx, cancel := chromedp.NewContext(context.Background())
	defer cancel()

	// start the browser without a timeout
	if err := chromedp.Run(browserCtx); err != nil {
		slog.Error("start browser (chromedp.Run)", "err", err)
		os.Exit(10)
	}

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.Handle("/metrics", promhttp.Handler())
		slog.Info("metrics and profile listening on :8001")
		// go func() { log.Fatal(http.ListenAndServe(":6060", mux)) }()
		err := http.ListenAndServe(":8001", mux)
		slog.Error("listenAndServe", "err", err)
		os.Exit(2)
	}()

	e := echo.New()
	e.Add("GET", "/image/offset/:ip", offsetHandler(parentCtx, upstream), middleware.Logger())
	e.Add("GET", "/__health", func(c echo.Context) error {
		// todo: check that the browser actually works
		return c.String(200, "ok")
	})

	err := e.Start(":8000")
	if err != nil {
		slog.Error("echo Start", "err", err)
		os.Exit(2)
	}

}

func offsetHandler(mainCtx context.Context, upstream string) func(echo.Context) error {
	return func(c echo.Context) error {
		ipStr := c.Param("ip")
		slog.Info("offsetHandler", "ip", ipStr)

		buf, err := takeScreenshot(mainCtx, upstream, ipStr)
		if err != nil {
			var hErr *httpErr
			if errors.As(err, &hErr) {
				if hErr.status == 404 {
					return c.String(404, "Not found")
				}
			}
			return c.String(500, err.Error())
		}

		return c.Blob(200, "image/png", buf)
	}
}

func takeScreenshot(mainCtx context.Context, upstream, ip string) ([]byte, error) {

	ctx, cancel := chromedp.NewContext(
		mainCtx,
		// chromedp.WithDebugf(log.Printf),
	)
	defer cancel()

	defer func() {
		if err := chromedp.Run(ctx, page.Close()); err != nil {
			slog.Error("could not close tab", "err", err)
		}
	}()

	url := fmt.Sprintf("%s/scores/%s?graph_only=1", upstream, ip)

	viewX := 233
	viewY := 501

	chromedp.EmulateReset()
	// retina / hidpi / 2x screenshot
	// emulateOpts := chromedp.EmulateScale(2)

	resp, err := chromedp.RunResponse(ctx, chromedp.Tasks{
		chromedp.Navigate(url),
		chromedp.EmulateViewport(int64(viewY), int64(viewX)), // emulateOpts),
	})
	if err != nil {
		return nil, err
	}
	if resp.Status != 200 {
		if resp.Status == 404 {
			return nil, &httpErr{status: 404}
		}
		slog.Info("invalid response status", "status", resp.Status, "url", url)
		return nil, fmt.Errorf("response status %d", resp.Status)
	}

	// capture screenshot of an element
	var buf []byte

	if err = chromedp.Run(ctx, chromedp.Tasks{
		chromedp.WaitReady("#loaded", chromedp.NodeVisible),
		chromedp.Sleep(100 * time.Millisecond),
		chromedp.Screenshot(`#graph`, &buf, chromedp.NodeVisible),
	}); err != nil {
		slog.ErrorCtx(ctx, "screenshot", "err", err)
	}

	return buf, nil

}
