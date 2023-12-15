package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/labstack/echo/v4"
	"go.ntppool.org/common/logger"
	"go.ntppool.org/common/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func offsetHandler(mainCtx context.Context, upstream string) func(echo.Context) error {
	return func(c echo.Context) error {
		log := logger.Setup()

		ctx := c.Request().Context()

		span := trace.SpanFromContext(ctx)

		ipStr := c.Param("ip")
		log.InfoContext(ctx, "offsetHandler", "ip", ipStr)

		buf, err := takeScreenshot(mainCtx, ctx, upstream, ipStr)
		if err != nil {
			var hErr *httpErr
			if errors.As(err, &hErr) {
				if hErr.status == 404 {
					return c.String(404, "Not found")
				}
			}
			return c.String(500, err.Error())
		}

		if len(buf) == 0 {
			span.RecordError(fmt.Errorf("empty response"))
			return c.String(http.StatusBadGateway, "empty response")
		}

		return c.Blob(200, "image/png", buf)
	}
}

func takeScreenshot(mainCtx, reqCtx context.Context, upstream, ip string) ([]byte, error) {

	log := logger.Setup()
	reqCtx, span := tracing.Tracer().Start(
		reqCtx, "takeScreenshot",
	)
	defer span.End()

	ctx, cancel := chromedp.NewContext(
		mainCtx,
		// chromedp.WithDebugf(log.Printf),
	)
	defer cancel()

	traceID := span.SpanContext().TraceID()

	defer func() {
		span.AddEvent("closing tab", trace.WithAttributes(attribute.String("ctx.error", ctx.Err().Error())))
		if err := chromedp.Run(ctx, page.Close()); err != nil {
			log.ErrorContext(reqCtx, "could not close tab", "err", err, "trace_id", traceID.String())
			span.RecordError(err)
		}
	}()

	ctx, timeoutCancel := context.WithTimeout(ctx, 15*time.Second)
	defer timeoutCancel()

	url := fmt.Sprintf("%s/scores/%s?graph_only=1", upstream, ip)

	viewX := 233
	viewY := 501

	chromedp.EmulateReset()
	// retina / hidpi / 2x screenshot
	// emulateOpts := chromedp.EmulateScale(2)

	_, spanRun := tracing.Tracer().Start(
		reqCtx, "chromedp.RunResponse",
		trace.WithAttributes(attribute.String("url", url)),
	)
	resp, err := chromedp.RunResponse(ctx, chromedp.Tasks{
		network.Enable(),
		network.SetExtraHTTPHeaders(network.Headers(
			map[string]interface{}{
				"traceparent": traceID,
			},
		)),
		chromedp.Navigate(url),
		chromedp.EmulateViewport(int64(viewY), int64(viewX)), // emulateOpts),
	})
	if err != nil {
		spanRun.RecordError(err)
		spanRun.End()
		return nil, err
	}
	if resp.Status != 200 {
		if resp.Status == 404 {
			spanRun.End()
			return nil, &httpErr{status: 404}
		}
		err := fmt.Errorf("response status %d", resp.Status)
		spanRun.RecordError(err)
		log.WarnContext(reqCtx, "invalid response status", "status", resp.Status, "url", url)
		spanRun.End()
		return nil, err
	}
	spanRun.End()

	// only wait 3 seconds for the page to load
	loadingCtx, loadCancel := context.WithTimeout(ctx, 4*time.Second)
	defer loadCancel()

	_, spanLoad := tracing.Tracer().Start(
		reqCtx, "chromedp.WaitReady",
	)
	if err = chromedp.Run(loadingCtx, chromedp.Tasks{
		chromedp.WaitReady("#loaded", chromedp.NodeVisible),
		chromedp.Sleep(200 * time.Millisecond),
	}); err != nil {
		log.ErrorContext(loadingCtx, "loading", "err", err)
		spanLoad.RecordError(err)
		// don't return the error; just take a screenshot
		// and continue ...
	}
	spanLoad.End()

	// capture screenshot of an element
	var buf []byte

	_, spanShot := tracing.Tracer().Start(
		reqCtx, "chromedp.Screenshot",
	)
	if err = chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Screenshot(`#graph`, &buf, chromedp.NodeVisible),
	}); err != nil {
		log.ErrorContext(reqCtx, "screenshot", "err", err)
		spanShot.RecordError(err)
		spanShot.End()
		return nil, err
	}
	spanShot.End()

	return buf, nil
}
