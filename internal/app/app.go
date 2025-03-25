package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync/atomic"
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

	"net/http/pprof"

	"github.com/hoangtu1372k2/common-go/auth"
	"github.com/hoangtu1372k2/common-go/telemetry"
)

var (
	ErrShutdown = fmt.Errorf("application shutdown gracefully")
)

var srv *http.Server
var runCtx context.Context
var runCancel context.CancelFunc
var cfg *viper.Viper
var log *logrus.Logger
var stats = telemetry.New()
var limiter *RateLimiter

var globalRequestCount uint64
var globalLimit uint64

var TrustedProxies = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

// Run application.
func Run(c *viper.Viper) error {
	var err error

	runCtx, runCancel = context.WithCancel(context.Background())
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
		FixedLimit:   cfg.GetInt("rate_limit_fixed"),
		SlidingLimit: cfg.GetInt("rate_limit_sliding"),
		TokenLimit:   cfg.GetInt("rate_limit_tokens"),
		TokenRefill:  10 * time.Second,
		Window:       10 * time.Second,
	}
	limiter = NewRateLimiter(rateLimitCfg)

	if cfg.GetInt("global_limit") > 0 {
		globalLimit = uint64(cfg.GetInt("global_limit"))
	}

	// Cấu hình Swagger
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

	// Reset global request counter mỗi phút, kiểm soát bằng runCtx
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				log.Info("Stopping global request counter reset goroutine")
				return
			case <-ticker.C:
				atomic.StoreUint64(&globalRequestCount, 0)
			}
		}
	}()

	apiV0 := router.Group(cfg.GetString("service_path"))
	{
		var tokenRequired bool = true
		if strings.Contains(cfg.GetString("base_url"), "127.0.0.1") {
			tokenRequired = false
		}

		apiV0.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
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

		apiV0.GET("/health", handleWrapper(controllers.Health, tokenRequired))

		srv = &http.Server{
			Addr:    cfg.GetString("listen_addr"),
			Handler: router,
		}

		// Xử lý tín hiệu shutdown
		go func() {
			trap := make(chan os.Signal, 1)
			signal.Notify(trap, syscall.SIGTERM, syscall.SIGINT)
			s := <-trap
			log.Infof("Received shutdown signal %s", s)
			Stop()
		}()

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

// Stop is used to gracefully shutdown the server with a timeout.
func Stop() {
	// Tạo context với timeout 5 giây
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Shutdown server với timeout
	if err := srv.Shutdown(ctx); err != nil {
		log.Errorf("Failed to shutdown HTTP server gracefully: %s", err)
		// Nếu shutdown thất bại, đóng server ngay lập tức
		if err := srv.Close(); err != nil {
			log.Errorf("Failed to force close HTTP server: %s", err)
		}
	} else {
		log.Info("HTTP server shutdown gracefully")
	}

	// Hủy runCtx để dừng các goroutine liên quan
	runCancel()
}

var isPProf = regexp.MustCompile(`.*debug\/pprof.*`)

// Lấy IP đáng tin cậy từ request
func GetTrustedClientIP(c *gin.Context) string {
	// Lấy IP từ X-Forwarded-For
	xff := c.Request.Header.Get("X-Forwarded-For")
	if xff != "" {
		ips := strings.Split(xff, ",")
		for i := len(ips) - 1; i >= 0; i-- {
			ip := strings.TrimSpace(ips[i])
			if isTrustedProxy(ip) {
				continue
			}
			fmt.Println("IP X-Forwarded-For : ", ip)
			return ip
		}
	}
	// Fallback về RemoteAddr nếu không có proxy đáng tin cậy
	ip, _, _ := net.SplitHostPort(c.Request.RemoteAddr)
	fmt.Println("IP SplitHostPort : ", ip)
	return ip
}

// isTrustedProxy kiểm tra xem IP có thuộc danh sách proxy đáng tin cậy không
func isTrustedProxy(ip string) bool {
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}
	for _, cidr := range TrustedProxies {
		_, subnet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if subnet.Contains(parsedIP) {
			return true
		}
	}
	return false
}

func handleWrapper(n gin.HandlerFunc, tokenRequired bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := time.Now()

		// Lọc header nhạy cảm
		safeHeaders := make(http.Header)
		for k, v := range c.Request.Header {
			if k != "Authorization" && k != "Cookie" {
				safeHeaders[k] = v
			}
		}

		// Log request với header đã lọc
		log.WithFields(logrus.Fields{
			"method":         c.Request.Method,
			"remote-addr":    c.Request.RemoteAddr,
			"http-protocol":  c.Request.Proto,
			"headers":        safeHeaders, // Sử dụng safeHeaders thay vì c.Request.Header
			"content-length": c.Request.ContentLength,
		}).Debugf("HTTP Request to %s", c.Request.URL)

		// Kiểm tra PProf
		if isPProf.MatchString(c.Request.URL.Path) && !cfg.GetBool("enable_pprof") {
			log.WithFields(logrus.Fields{
				"method":         c.Request.Method,
				"remote-addr":    c.Request.RemoteAddr,
				"http-protocol":  c.Request.Proto,
				"headers":        safeHeaders,
				"content-length": c.Request.ContentLength,
			}).Debugf("Request to PProf Address failed, PProf disabled")
			c.AbortWithStatus(http.StatusForbidden)
			stats.Srv.WithLabelValues(c.Request.URL.Path).Observe(time.Since(now).Seconds())
			return
		}

		// Kiểm tra global rate limit
		currentCount := atomic.LoadUint64(&globalRequestCount)
		if currentCount >= globalLimit {
			log.Warnf("Global rate limit exceeded: %d requests", currentCount)
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "Server is under heavy load. Please try again later.",
			})
			return
		}
		atomic.AddUint64(&globalRequestCount, 1)

		// Xác định userID
		var userID string
		if tokenRequired {
			if !auth.ValidateToken(c, cfg.GetString("jwk_set_uri")) {
				c.AbortWithStatus(http.StatusUnauthorized)
				return
			}
			userID = auth.GetUserID(c)
		} else {
			userID = GetTrustedClientIP(c)
		}

		// Rate Limiting với Redis
		if limiter != nil {
			ctx := c.Request.Context()
			if !limiter.TokenBucket(ctx, userID) {
				log.Warnf("Rate Limit exceeded for user: %s", userID)
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"error": "Rate limit exceeded. Please try again later.",
				})
				return
			}
		}

		n(c)
		stats.Srv.WithLabelValues(c.Request.URL.Path).Observe(time.Since(now).Seconds())
	}
}
