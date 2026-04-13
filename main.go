package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Route maps a path prefix to a backend URL.
type Route struct {
	Prefix  string `yaml:"prefix"`
	Backend string `yaml:"backend"`
}

// Config holds the top-level configuration.
type Config struct {
	Listen string  `yaml:"listen"` // e.g. ":443" or ":8080"
	Routes []Route `yaml:"routes"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	return &cfg, nil
}

// newDirectProxy creates a reverse proxy that forwards requests as-is (no prefix stripping).
func newDirectProxy(rawBackend string) (*httputil.ReverseProxy, error) {
	target, err := url.Parse(rawBackend)
	if err != nil {
		return nil, fmt.Errorf("invalid backend URL %q: %w", rawBackend, err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Header.Set("X-Forwarded-Host", req.Host)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error [-> %s]: %v", rawBackend, err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}
	return proxy, nil
}

// newReverseProxy creates a reverse proxy that strips the prefix before forwarding.
func newReverseProxy(prefix, rawBackend string) (http.Handler, error) {
	target, err := url.Parse(rawBackend)
	if err != nil {
		return nil, fmt.Errorf("invalid backend URL %q: %w", rawBackend, err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// Preserve the original Director and strip the prefix on top.
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)

		// Strip the route prefix so the backend sees /  instead of /e1/...
		stripped := strings.TrimPrefix(req.URL.Path, prefix)
		if stripped == "" {
			stripped = "/"
		}
		req.URL.Path = stripped
		if req.URL.RawPath != "" {
			rawStripped := strings.TrimPrefix(req.URL.RawPath, prefix)
			if rawStripped == "" {
				rawStripped = "/"
			}
			req.URL.RawPath = rawStripped
		}

		// Forward the original host so backends can use it if needed.
		req.Header.Set("X-Forwarded-Host", req.Host)
		req.Header.Set("X-Forwarded-Prefix", prefix)
	}

	// Rewrite Location headers in redirect responses so they go back through the proxy.
	proxy.ModifyResponse = func(resp *http.Response) error {
		loc := resp.Header.Get("Location")
		if loc == "" {
			return nil
		}
		// Only rewrite absolute paths (relative to the backend root).
		if strings.HasPrefix(loc, "/") {
			resp.Header.Set("Location", prefix+loc)
		}
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error [%s -> %s]: %v", prefix, rawBackend, err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	return proxy, nil
}

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	mux := http.NewServeMux()

	for _, route := range cfg.Routes {
		prefix := route.Prefix
		backend := route.Backend

		// Ensure prefix starts with /
		if !strings.HasPrefix(prefix, "/") {
			prefix = "/" + prefix
		}

		proxy, err := newReverseProxy(prefix, backend)
		if err != nil {
			log.Fatalf("failed to create proxy for route %s -> %s: %v", prefix, backend, err)
		}

		// http.ServeMux requires trailing slash for subtree matching
		pattern := prefix
		if !strings.HasSuffix(pattern, "/") {
			pattern += "/"
		}

		log.Printf("route: %s%s  ->  %s", cfg.Listen, prefix, backend)
		mux.Handle(pattern, http.StripPrefix(prefix, proxy))

		// Also match the exact prefix without trailing slash
		exactPrefix := strings.TrimSuffix(prefix, "/")
		if exactPrefix != "" {
			mux.Handle(exactPrefix, http.RedirectHandler(exactPrefix+"/", http.StatusMovedPermanently))
		}
	}

	// Health check endpoint
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	// Build a map of prefix -> direct proxy for Referer-based fallback routing.
	refererProxies := make(map[string]*httputil.ReverseProxy)
	for _, route := range cfg.Routes {
		prefix := route.Prefix
		if !strings.HasPrefix(prefix, "/") {
			prefix = "/" + prefix
		}
		dp, err := newDirectProxy(route.Backend)
		if err != nil {
			log.Fatalf("failed to create direct proxy for %s: %v", route.Backend, err)
		}
		refererProxies[prefix] = dp
	}

	// Catch-all: if the Referer matches a known route prefix, proxy to that backend.
	// Otherwise, list available routes or 404.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		referer := r.Header.Get("Referer")
		if referer != "" {
			if refURL, err := url.Parse(referer); err == nil {
				for prefix, proxy := range refererProxies {
					if strings.HasPrefix(refURL.Path, prefix) {
						log.Printf("referer fallback: %s -> %s (via %s)", r.URL.Path, prefix, referer)
						proxy.ServeHTTP(w, r)
						return
					}
				}
			}
		}

		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "Available routes:")
		for _, route := range cfg.Routes {
			fmt.Fprintf(w, "  %s  ->  %s\n", route.Prefix, route.Backend)
		}
	})

	log.Printf("starting proxy on %s", cfg.Listen)
	if err := http.ListenAndServe(cfg.Listen, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
