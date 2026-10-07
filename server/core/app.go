package core

import (
	"github.com/hasan-muzak/keel/pkg/keel"
	"github.com/hasan-muzak/keel/pkg/llm"
)

// App is Promptdam on top of keel: the shared services plus Promptdam's own.
type App struct {
	*keel.Core
	// Prices cost model calls.
	Prices *llm.Catalog
	// GatewayURL is where clients reach the gateway, for setup instructions.
	GatewayURL string
}

// Option configures the App.
type Option func(*App)

// WithPrices uses catalog for model prices instead of the embedded one.
func WithPrices(catalog *llm.Catalog) Option {
	return func(a *App) { a.Prices = catalog }
}

// WithGatewayURL sets where clients reach the gateway.
func WithGatewayURL(url string) Option {
	return func(a *App) { a.GatewayURL = url }
}

// NewApp wraps keel's services with Promptdam's.
func NewApp(c *keel.Core, opts ...Option) *App {
	a := &App{Core: c}
	for _, o := range opts {
		o(a)
	}
	if a.Prices == nil {
		a.Prices = llm.Default()
	}
	if a.GatewayURL == "" {
		a.GatewayURL = "http://localhost:8282"
	}
	return a
}
