package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/hoangtu1372k2/vms/docs/swagger"
	controllers "github.com/hoangtu1372k2/vms/internal/controller"

	cors "github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	"net/http"
	"net/http/pprof"

	"github.com/hoangtu1372k2/common-go/auth"
	"github.com/hoangtu1372k2/common-go/telemetry"
)

var (
	ErrShutdown = fmt.Errorf("application shutdown gracefully")
)

// srv is the global reference for the HTTP Server.
var srv *http.Server

// runCtx is a global context used to control shutdown of the application.
var runCtx context.Context

// runCancel is a global context cancelFunc used to trigger the shutdown of applications.
var runCancel context.CancelFunc

// cfg is used across the app package to contain configuration.
var cfg *viper.Viper

// log is used across the app package for logging.
var log *logrus.Logger

// stats is used across the app package to manage and access system metrics.
var stats = telemetry.New()

// Khởi tạo RateLimiter
var limiter *RateLimiter

// Run application.
func Run(c *viper.Viper) error {
	var err error

	// Create App Context
	runCtx, runCancel = context.WithCancel(context.Background())

	// Apply config provided by config package global
	cfg = c

	log = logrus.New()
	if cfg.GetBool("debug") {
		log.Level = logrus.DebugLevel
		log.Debug("Enabling Debug Logging")
	}
	if cfg.GetBool("trace") {
		log.Level = logrus.TraceLevel
		log.Debug("Enabling Trace Logging")
	}
	if cfg.GetBool("disable_logging") {
		log.Level = logrus.FatalLevel
	}

	// Khởi tạo Rate Limiter
	rateLimitCfg := RateLimitConfig{
		FixedLimit:   cfg.GetInt("rate_limit_fixed"),   // Ví dụ: 5 request/phút
		SlidingLimit: cfg.GetInt("rate_limit_sliding"), // Ví dụ: 5 request/phút trượt
		TokenLimit:   cfg.GetInt("rate_limit_tokens"),  // Ví dụ: 3 token
		TokenRefill:  10 * time.Second,                 // 100 token mỗi giây
		Window:       10 * time.Second,
	}
	limiter = NewRateLimiter(rateLimitCfg)

	//Thông tin swagger
	swagger.SwaggerInfo.Title = "VMS"
	swagger.SwaggerInfo.Description = "A video management service API."
	swagger.SwaggerInfo.Version = "1.0"
	swagger.SwaggerInfo.Host = cfg.GetString("base_url")
	swagger.SwaggerInfo.BasePath = cfg.GetString("service_path")
	if strings.Contains(cfg.GetString("service_path"), "127.0.0.1") || strings.Contains(cfg.GetString("service_path"), "localhost") {
		swagger.SwaggerInfo.Schemes = []string{"https", "http"}
	} else {
		swagger.SwaggerInfo.Schemes = []string{"http", "https"}
	}

	// Setting router
	gin.SetMode(gin.ReleaseMode)
	router := gin.Default()
	router.Use(cors.New(cors.Config{
		AllowCredentials:       true,
		AllowWildcard:          true,
		AllowBrowserExtensions: true,
		AllowOrigins:           []string{"*"},
		AllowMethods:           []string{"POST", "PUT", "PATCH", "DELETE", "GET", "OPTIONS", "UPDATE"},
		AllowHeaders: []string{
			"Content-Type, content-length, accept-encoding, X-CSRF-Token, " +
				"access-control-allow-origin, Authorization, X-Max, access-control-allow-headers, " +
				"accept, origin, Cache-Control, X-Requested-With, X-Request-Source"},
		MaxAge: 12 * time.Hour,
	}))

	apiV0 := router.Group(cfg.GetString("service_path"))
	{
		var tokenRequired bool = true
		if strings.Contains(cfg.GetString("base_url"), "127.0.0.1") {
			tokenRequired = false
		}

		// swagger
		apiV0.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

		// pprof handlers
		apiV0.GET("/debug/pprof/", gin.WrapH(http.HandlerFunc(pprof.Index)))
		apiV0.GET("/debug/pprof/cmdline", gin.WrapH(http.HandlerFunc(pprof.Cmdline)))
		apiV0.GET("/debug/pprof/profile", gin.WrapH(http.HandlerFunc(pprof.Profile)))
		apiV0.GET("/debug/pprof/symbol", gin.WrapH(http.HandlerFunc(pprof.Symbol)))
		apiV0.GET("/debug/pprof/trace", gin.WrapH(http.HandlerFunc(pprof.Trace)))
		apiV0.GET("/debug/pprof/allocs", gin.WrapH(pprof.Handler("allocs")))
		apiV0.GET("/debug/pprof/mutex", gin.WrapH(pprof.Handler("mutex")))
		apiV0.GET("/debug/pprof/goroutine", gin.WrapH(pprof.Handler("goroutine")))
		apiV0.GET("/debug/pprof/heap", gin.WrapH(pprof.Handler("heap")))
		apiV0.GET("/debug/pprof/threadcreate", gin.WrapH(pprof.Handler("threadcreate")))
		apiV0.GET("/debug/pprof/block", gin.WrapH(pprof.Handler("block")))

		// health check
		apiV0.GET("/health", handleWrapper(controllers.Health, tokenRequired))

		// Setup the HTTP Server
		srv = &http.Server{
			Addr:    cfg.GetString("listen_addr"),
			Handler: router,
		}

		// Kick off Graceful Shutdown Go Routine
		go func() {
			trap := make(chan os.Signal, 1)
			signal.Notify(trap, syscall.SIGTERM)
			s := <-trap
			log.Infof("Received shutdown signal %s", s)
			defer Stop()
		}()

		// Start HTTP Listener
		log.Infof("Starting HTTP Listener on %s, service path is %s", cfg.GetString("listen_addr"), cfg.GetString("service_path"))
		err = srv.ListenAndServe()
		if err != nil {
			if err == http.ErrServerClosed {
				<-runCtx.Done()
				return ErrShutdown
			}
			return fmt.Errorf("unable to start HTTP Server - %s", err)
		}

		return nil
	}
}

// Stop is used to gracefully shutdown the server.
func Stop() {
	err := srv.Shutdown(context.Background())
	if err != nil {
		log.Errorf("Unexpected error while shutting down HTTP server - %s", err)
	}
	defer runCancel()
}

// isPProf is a regex that validates if the given path is used for PProf
var isPProf = regexp.MustCompile(`.*debug\/pprof.*`)

// middleware is used to intercept incoming HTTP calls and apply general functions upon them.
func handleWrapper(n gin.HandlerFunc, tokenRequired bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := time.Now()

		// Log cơ bản
		log.WithFields(logrus.Fields{
			"method":         c.Request.Method,
			"remote-addr":    c.Request.RemoteAddr,
			"http-protocol":  c.Request.Proto,
			"headers":        c.Request.Header,
			"content-length": c.Request.ContentLength,
		}).Debugf("HTTP Request to %s", c.Request.URL)

		// Chặn PProf nếu disable
		if isPProf.MatchString(c.Request.URL.Path) && !cfg.GetBool("enable_pprof") {
			log.WithFields(logrus.Fields{
				"method":         c.Request.Method,
				"remote-addr":    c.Request.RemoteAddr,
				"http-protocol":  c.Request.Proto,
				"headers":        c.Request.Header,
				"content-length": c.Request.ContentLength,
			}).Debugf("Request to PProf Address failed, PProf disabled")
			c.AbortWithStatus(http.StatusForbidden)
			stats.Srv.WithLabelValues(c.Request.URL.Path).Observe(time.Since(now).Seconds())
			return
		}

		// Xác định userID
		var userID string
		if tokenRequired {
			if !auth.ValidateToken(c, cfg.GetString("jwk_set_uri")) {
				c.AbortWithStatus(http.StatusUnauthorized)
				return
			}
			userID = auth.GetUserID(c) // Lấy userID từ token JWT
		} else {
			userID = c.ClientIP() // Fallback về IP nếu không yêu cầu token
		}

		// Rate Limiting với Redis
		if limiter != nil {
			ctx := c.Request.Context()
			// Kiểm tra cả 3 thuật toán (hoặc chọn 1 tùy nhu cầu)
			if !limiter.TokenBucket(ctx, userID) {
				// || !limiter.SlidingWindow(ctx, userID)
				// || !limiter.TokenBucket(ctx, userID)
				// || !limiter.FixedWindow(ctx, userID)
				log.Warnf("Rate Limit exceeded for user: %s", userID)
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"error": "Rate limit exceeded. Please try again later.",
				})
				return
			}
		}

		// Thực thi handler chính
		n(c)
		stats.Srv.WithLabelValues(c.Request.URL.Path).Observe(time.Since(now).Seconds())
	}
}
