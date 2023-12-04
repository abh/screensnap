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
	slogecho "github.com/samber/slog-echo"
	"go.ntppool.org/common/logger"
	"go.ntppool.org/common/tracing"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

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
	log := logger.Setup()

	srvContext := context.Background()

	tpShutdown, err := tracing.InitTracer(srvContext, &tracing.TracerConfig{
		ServiceName: "screensnap",
	})
	if err != nil {
		log.ErrorContext(srvContext, "tracing setup", "err", err)
		os.Exit(2)
	}

	upstream := os.Getenv("upstream_base")
	if len(upstream) == 0 {
		log.ErrorContext(srvContext, "ntppool upstream_base not set")
		time.Sleep(1 * time.Second)
		os.Exit(2)
	}
	upstream = strings.TrimSuffix(upstream, "/")

	opts := append(
		chromedp.DefaultExecAllocatorOptions[:],
		chromedp.DisableGPU,
	)
	// create chromedp's context
	parentCtx, execCancel := chromedp.NewExecAllocator(srvContext, opts...)
	defer execCancel()

	// create a new browser
	browserCtx, cancel := chromedp.NewContext(context.Background())
	defer cancel()

	// start the browser without a timeout
	if err := chromedp.Run(browserCtx); err != nil {
		log.ErrorContext(srvContext, "start browser (chromedp.Run)", "err", err)
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
	e.Use(otelecho.Middleware("screensnap"))
	e.Use(slogecho.NewWithConfig(log,
		slogecho.Config{
			WithTraceID: true,
			WithSpanID:  true,
			// WithRequestHeader: true,
		},
	))
	e.Use(echo.WrapMiddleware(
		otelhttp.NewMiddleware("screensnap",
			otelhttp.WithMessageEvents(otelhttp.ReadEvents, otelhttp.WriteEvents),
		),
	))

	e.Use(
		func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				request := c.Request()

				span := trace.SpanFromContext(request.Context())
				c.Response().Header().Set("Traceparent", span.SpanContext().TraceID().String())

				return next(c)
			}
		},
	)

	e.Add("GET", "/image/offset/:ip", offsetHandler(parentCtx, upstream), middleware.Logger())
	e.Add("GET", "/__health", func(c echo.Context) error {
		// todo: check that the browser actually works
		return c.String(200, "ok")
	})

	go func() {
		err := e.Start(":8000")
		if err != nil {
			log.Error("echo Start", "err", err)
		}
	}()

	<-srvContext.Done()

	shutdownCtx, cancel := context.WithTimeout(srvContext, 15*time.Second)
	defer cancel()
	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Warn("could not shutdown echo", "err", err)
	}

	shutdownCtx, tpCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer tpCancel()
	err = tpShutdown(shutdownCtx)
	if err != nil {
		log.Warn("could not shutdown tracer", "err", err)
	}

}

func offsetHandler(mainCtx context.Context, upstream string) func(echo.Context) error {
	return func(c echo.Context) error {
		ipStr := c.Param("ip")
		slog.Info("offsetHandler", "ip", ipStr)

		buf, err := takeScreenshot(mainCtx, c.Request().Context(), upstream, ipStr)
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

	defer func() {
		span.AddEvent("closing tab")
		if err := chromedp.Run(ctx, page.Close()); err != nil {
			log.ErrorContext(reqCtx, "could not close tab", "err", err)
			span.RecordError(err)
		}
	}()

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

	// capture screenshot of an element
	var buf []byte

	reqCtx, spanShot := tracing.Tracer().Start(
		reqCtx, "chromedp.Screenshot",
	)
	if err = chromedp.Run(ctx, chromedp.Tasks{
		chromedp.WaitReady("#loaded", chromedp.NodeVisible),
		chromedp.Sleep(100 * time.Millisecond),
		chromedp.Screenshot(`#graph`, &buf, chromedp.NodeVisible),
	}); err != nil {
		log.ErrorContext(reqCtx, "screenshot", "err", err)
		spanShot.RecordError(err)
	}
	spanShot.End()

	return buf, nil

}
