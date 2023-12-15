package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"net/http"

	slogecho "github.com/samber/slog-echo"
	"go.ntppool.org/common/health"
	"go.ntppool.org/common/logger"
	"go.ntppool.org/common/metricsserver"
	"go.ntppool.org/common/tracing"
	"go.ntppool.org/common/version"
	"go.ntppool.org/common/xff/fastlyxff"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/chromedp/chromedp"
	"github.com/labstack/echo/v4"
	"golang.org/x/sync/errgroup"
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

	healthSrv := health.NewServer(func(w http.ResponseWriter, r *http.Request) {
		// todo: the browser is working
		w.WriteHeader(200)
		w.Write([]byte("ok!"))
	})

	g, ctx := errgroup.WithContext(srvContext)
	g.Go(func() error {
		healthSrv.SetLogger(log.WithGroup("health"))
		return healthSrv.Listen(ctx, 8002)
	})

	metricssrv := metricsserver.New()
	g.Go(func() error {
		return metricssrv.ListenAndServe(ctx, 8001)
	})
	version.RegisterMetric("screensnap", metricssrv.Registry())

	e := echo.New()

	g.Go(func() error {
		return RunAPI(e, parentCtx)
	})

	g.Go(func() error {
		<-srvContext.Done()
		return srvContext.Err()
	})

	err = g.Wait()
	if err != nil {
		log.ErrorContext(srvContext, "server shutdown", "err", err)
	}

	shutdownCtx, cancel := context.WithTimeout(srvContext, 10*time.Second)
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

func RunAPI(e *echo.Echo, parentCtx context.Context) error {
	log := logger.Setup()

	trustOptions := []echo.TrustOption{
		echo.TrustLoopback(true),
		echo.TrustLinkLocal(false),
		echo.TrustPrivateNet(true),
	}

	if fileName := os.Getenv("FASTLY_IPS"); len(fileName) > 0 {
		xff, err := fastlyxff.New(fileName)
		if err != nil {
			return err
		}
		cdnTrustRanges, err := xff.EchoTrustOption()
		if err != nil {
			return err
		}
		trustOptions = append(trustOptions, cdnTrustRanges...)
	} else {
		log.Warn("Fastly IPs not configured (FASTLY_IPS)")
	}

	e.IPExtractor = echo.ExtractIPFromXFFHeader(trustOptions...)

	e.Use(otelecho.Middleware("screensnap"))

	e.Use(
		func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				request := c.Request()

				span := trace.SpanFromContext(request.Context())
				span.SetAttributes(attribute.String("http.real_ip", c.RealIP()))

				// since the Go library (temporarily?) isn't including this
				span.SetAttributes(attribute.String("url.path", c.Path()))
				if q := c.QueryString(); len(q) > 0 {
					span.SetAttributes(attribute.String("url.query", q))
				}

				c.Response().Header().Set("Traceparent", span.SpanContext().TraceID().String())

				return next(c)
			}
		},
	)

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

	upstream := os.Getenv("upstream_base")
	if len(upstream) == 0 {
		return fmt.Errorf("ntppool upstream_base env not set")
	}
	upstream = strings.TrimSuffix(upstream, "/")

	e.Add("GET", "/image/offset/:ip", offsetHandler(parentCtx, upstream))
	e.Add("GET", "/__health", func(c echo.Context) error {
		// todo: check that the browser actually works
		return c.String(200, "ok")
	})

	err := e.Start(":8000")
	if err != nil {
		return fmt.Errorf("echo: %w", err)
	}

	return nil
}
