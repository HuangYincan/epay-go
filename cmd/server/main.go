// cmd/server/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/example/epay-go/internal/cache"
	"github.com/example/epay-go/internal/config"
	"github.com/example/epay-go/internal/database"
	"github.com/example/epay-go/internal/middleware"
	"github.com/example/epay-go/internal/router"
	"github.com/example/epay-go/internal/service"
	"github.com/gin-gonic/gin"

	// 注册支付适配器
	_ "github.com/example/epay-go/internal/payment"
)

func main() {
	// 加载配置
	if err := config.Load("config.yaml"); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	cfg := config.Get()

	// 初始化数据库
	if err := database.Init(); err != nil {
		log.Fatalf("Failed to init database: %v", err)
	}
	defer database.Close()

	// 数据库迁移
	if err := database.Migrate(); err != nil {
		log.Fatalf("Failed to migrate database: %v", err)
	}

	if len(os.Args) > 1 {
		if len(os.Args) == 2 && os.Args[1] == "migrate" {
			return
		}
		log.Fatal("unknown command; supported: migrate")
	}
	// 初始化 Redis
	if err := cache.Init(); err != nil {
		log.Fatalf("Failed to init redis: %v", err)
	}
	defer cache.Close()

	// 初始化默认管理员
	adminService := service.NewAdminService()
	if err := adminService.InitDefaultAdmin(); err != nil {
		log.Fatalf("Failed to init default admin: %v", err)
	}

	// 设置 Gin 模式
	gin.SetMode(cfg.Server.Mode)

	// 创建 Gin 引擎
	r := gin.New()

	// 全局中间件
	r.Use(middleware.Recovery())
	r.Use(middleware.Logger())
	r.Use(middleware.Cors())

	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// 注册所有路由
	router.Setup(r)

	// 启动异步通知工作协程
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	notifyService := service.NewNotifyService()
	go notifyService.StartNotifyWorker(ctx)

	// 启动订单主动查单补偿工作协程
	orderQueryService := service.NewOrderQueryService()
	go orderQueryService.StartQueryWorker(ctx)
	go service.NewRefundService().StartWorker(ctx)

	// 创建 HTTP 服务器
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// 在 goroutine 中启动服务器
	go func() {
		log.Printf("Server starting on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// 取消 notify worker 的 context
	cancel()

	// 优雅关闭 HTTP 服务器（最多等待 5 秒）
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}
