package httpclientprovider

import (
	"fmt"
	"net/http"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend/httpclient"
	sdkhttpclient "github.com/grafana/grafana-plugin-sdk-go/backend/httpclient"
	httplogger "github.com/grafana/grafana-plugin-sdk-go/experimental/http_logger"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/infra/metrics/metricutil"
	"github.com/grafana/grafana/pkg/infra/tracing"
	"github.com/grafana/grafana/pkg/setting"
	"github.com/mwitkow/go-conntrack"
)

var newProviderFunc = sdkhttpclient.NewProvider

// New creates a new HTTP client provider with pre-configured middlewares.
func New(cfg *setting.Cfg, tracer tracing.Tracer) *sdkhttpclient.Provider {
	logger := log.New("httpclient")
	userAgent := fmt.Sprintf("Grafana/%s", cfg.BuildVersion)

	middlewares := []sdkhttpclient.Middleware{
		TracingMiddleware(logger, tracer),
		DataSourceMetricsMiddleware(),
		httpLoggerMiddleware(),
		SetUserAgentMiddleware(userAgent),
		sdkhttpclient.BasicAuthenticationMiddleware(),
		sdkhttpclient.CustomHeadersMiddleware(),
		ResponseLimitMiddleware(cfg.ResponseLimit),
	}

	if cfg.SigV4AuthEnabled {
		middlewares = append(middlewares, SigV4Middleware(cfg.SigV4VerboseLogging))
	}

	setDefaultTimeoutOptions(cfg)

	return newProviderFunc(sdkhttpclient.ProviderOptions{
		Middlewares: middlewares,
		ConfigureTransport: func(opts sdkhttpclient.Options, transport *http.Transport) {
			datasourceName, exists := opts.Labels["datasource_name"]
			if !exists {
				return
			}
			datasourceLabelName, err := metricutil.SanitizeLabelName(datasourceName)

			if err != nil {
				return
			}
			newConntrackRoundTripper(datasourceLabelName, transport)
		},
	})
}

// newConntrackRoundTripper takes a http.DefaultTransport and adds the Conntrack Dialer
// so we can instrument outbound connections
func newConntrackRoundTripper(name string, transport *http.Transport) *http.Transport {
	transport.DialContext = conntrack.NewDialContextFunc(
		conntrack.DialWithName(name),
		conntrack.DialWithDialContextFunc(transport.DialContext),
	)
	return transport
}

func httpLoggerMiddleware() httpclient.Middleware {
	return httpclient.NamedMiddlewareFunc("http-logger", func(opts httpclient.Options, next http.RoundTripper) http.RoundTripper {
		datasourceName, exists := opts.Labels["datasource_name"]
		if !exists {
			return next
		}

		harLogEnabled, exists := opts.Labels["har_log_enabled"]
		if !exists || harLogEnabled != "true" {
			return next
		}

		hl := httplogger.
			NewHTTPLogger(datasourceName, next).
			WithEnabledCheck(func() bool { return true })

		harLogPath, exists := opts.Labels["har_log_path"]
		if exists {
			hl = hl.WithPath(harLogPath)
		}

		return hl
	})
}

// setDefaultTimeoutOptions overrides the default timeout options for the SDK.
//
// Note: Not optimal changing global state, but hard to not do in this case.
func setDefaultTimeoutOptions(cfg *setting.Cfg) {
	sdkhttpclient.DefaultTimeoutOptions = sdkhttpclient.TimeoutOptions{
		Timeout:               time.Duration(cfg.DataProxyTimeout) * time.Second,
		DialTimeout:           time.Duration(cfg.DataProxyDialTimeout) * time.Second,
		KeepAlive:             time.Duration(cfg.DataProxyKeepAlive) * time.Second,
		TLSHandshakeTimeout:   time.Duration(cfg.DataProxyTLSHandshakeTimeout) * time.Second,
		ExpectContinueTimeout: time.Duration(cfg.DataProxyExpectContinueTimeout) * time.Second,
		MaxConnsPerHost:       cfg.DataProxyMaxConnsPerHost,
		MaxIdleConns:          cfg.DataProxyMaxIdleConns,
		MaxIdleConnsPerHost:   cfg.DataProxyMaxIdleConns,
		IdleConnTimeout:       time.Duration(cfg.DataProxyIdleConnTimeout) * time.Second,
	}
}
