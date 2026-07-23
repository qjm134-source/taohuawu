package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/watertown/guide/internal/agent"
	"github.com/watertown/guide/internal/agent/tools"
	"github.com/watertown/guide/internal/config"
	"github.com/watertown/guide/internal/cost"
	"github.com/watertown/guide/internal/database"
	"github.com/watertown/guide/internal/emotion"
	"github.com/watertown/guide/internal/knowledge"
	"github.com/watertown/guide/internal/llm"
	"github.com/watertown/guide/internal/observability"
	"github.com/watertown/guide/internal/weather"
	"github.com/watertown/guide/internal/websocket"
	"github.com/watertown/guide/pkg/logging"
	"github.com/watertown/guide/pkg/utils"
	"gorm.io/gorm"
)

// Server HTTP 服务器
type Server struct {
	config         *config.Config
	router         *gin.Engine
	wsHandler      *WebSocketHandler
	server         *http.Server
	db             *gorm.DB
	auditRepo      database.AuditRepository
	logger         logging.Logger
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc

	// Agent 组件
	agentHub       *websocket.Hub
	agentRuntime   *agent.Runtime
	sessionManager *agent.SessionManager
}

// New 创建服务器
func New(cfg *config.Config, db *gorm.DB, kb interface{}, logger logging.Logger) (*Server, error) {
	// 参数校验
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	if db == nil {
		return nil, fmt.Errorf("db is nil")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger is nil")
	}

	// 创建关闭上下文
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())

	// 创建审计日志仓库
	auditRepo := database.NewAuditRepository(db)

	s := &Server{
		config:         cfg,
		db:             db,
		auditRepo:      auditRepo,
		logger:         logger,
		shutdownCtx:    shutdownCtx,
		shutdownCancel: shutdownCancel,
	}

	s.setupRouter()
	if err := s.initAgentComponents(kb); err != nil {
		return nil, fmt.Errorf("failed to init agent components: %w", err)
	}

	return s, nil
}

// setupRouter 设置路由
func (s *Server) setupRouter() {
	if s.config.Logging.Level == "debug" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	s.router = gin.New()
	s.router.Use(gin.Recovery())
	s.router.Use(observability.TracingMiddleware(s.config.Observability.ServiceName))

	// 根据配置决定是否启用 Prometheus
	if s.config.Observability.Prometheus {
		s.router.Use(observability.PrometheusMiddleware())
	}

	s.router.Use(s.loggingMiddleware())
	s.router.Use(s.corsMiddleware())

	// 健康检查
	s.router.GET("/health", s.healthCheck)

	// Prometheus 指标（根据配置启用）
	if s.config.Observability.Prometheus {
		s.router.GET("/metrics", s.getMetrics)
	}

	// 审计日志 API
	api := s.router.Group("/api/v1")
	{
		api.GET("/audit", s.getAuditLogs)
	}

	// WebSocket
	s.router.GET(s.config.WebSocket.Path, s.handleWebSocket)

	// 静态文件服务 - 前端页面
	s.router.Static("/css", "./frontend/css")
	s.router.Static("/js", "./frontend/js")
	s.router.Static("/assets", "./frontend/assets")
	s.router.StaticFile("/", "./frontend/index.html")
	s.router.StaticFile("/favicon.ico", "./frontend/assets/favicon.ico")
	s.router.NoRoute(func(c *gin.Context) {
		c.File("./frontend/index.html")
	})
}

// Start 启动服务器
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.config.Server.Port)
	s.logger.Info("Starting server", "address", addr)

	server := &http.Server{
		Addr:         addr,
		Handler:      s.router,
		ReadTimeout:  s.config.Server.ReadTimeout.Duration,
		WriteTimeout: s.config.Server.WriteTimeout.Duration,
	}

	s.server = server

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}

	return nil
}

// Shutdown 关闭服务器
func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("Shutting down server...")
	s.shutdownCancel()

	// 给服务器 10 秒时间关闭
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := s.server.Shutdown(shutdownCtx); err != nil {
		return err
	}

	s.logger.Info("Server shutdown complete")
	return nil
}

// healthCheck 健康检查
func (s *Server) healthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"version": "1.0.0",
		"time":    time.Now().Unix(),
	})
}

// getMetrics 暴露 Prometheus 指标
func (s *Server) getMetrics(c *gin.Context) {
	promhttp.Handler().ServeHTTP(c.Writer, c.Request)
}

// getAuditLogs 获取审计日志
func (s *Server) getAuditLogs(c *gin.Context) {
	tenantID := c.Query("tenantId")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tenantId is required"})
		return
	}

	pageStr := c.DefaultQuery("page", "1")
	page, _ := strconv.Atoi(pageStr)
	if page < 1 {
		page = 1
	}

	pageSizeStr := c.DefaultQuery("pageSize", "20")
	pageSize, _ := strconv.Atoi(pageSizeStr)
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	logs, total, err := s.auditRepo.GetByTenantID(tenantID, time.Time{}, time.Time{}, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
		"logs":     logs,
	})
}

// loggingMiddleware 日志中间件
func (s *Server) loggingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		// 跳过 health 和 metrics 路径的日志记录（太频繁）
		path := c.Request.URL.Path
		if path == "/health" || path == "/metrics" {
			return
		}

		duration := time.Since(start)
		s.logger.Info("HTTP request",
			"method", c.Request.Method,
			"path", path,
			"status", c.Writer.Status(),
			"duration", duration,
		)
	}
}

// corsMiddleware CORS 中间件
func (s *Server) corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// handleWebSocket 处理 WebSocket
func (s *Server) handleWebSocket(c *gin.Context) {
	if s.wsHandler == nil {
		s.logger.Error("WebSocket handler is nil")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "WebSocket handler not initialized"})
		return
	}
	s.wsHandler.Handle(c)
}

// SetWebSocketHandler 设置 WebSocket 处理器
func (s *Server) SetWebSocketHandler(handler *WebSocketHandler) {
	s.wsHandler = handler
}

// initAgentComponents 初始化 Agent 相关组件。
// 将各组件创建逻辑拆分为独立方法，降低单函数复杂度并提升可测试性。
func (s *Server) initAgentComponents(kb interface{}) error {
	knowledgeBase, err := s.initKnowledgeBase(kb)
	if err != nil {
		return err
	}

	s.agentHub = s.initWebSocketHub()
	s.sessionManager = agent.NewSessionManager()

	weatherService, err := s.initWeatherService()
	if err != nil {
		s.logger.Warn("Failed to create weather service", "error", err)
	}

	toolRegistry, err := s.initToolRegistry(knowledgeBase, weatherService)
	if err != nil {
		return fmt.Errorf("init tool registry: %w", err)
	}

	llmAdapter, fallbackAdapter := s.initLLMAdapters(toolRegistry)
	embeddingAPI := s.initEmbeddingAPI()
	summarizer := s.initSummarizer(llmAdapter)
	optimizer := s.initOptimizer(embeddingAPI, summarizer)

	emotionDetector := emotion.NewRuleBasedDetector()
	s.agentRuntime = s.initRuntime(llmAdapter, fallbackAdapter, toolRegistry, optimizer, emotionDetector)
	s.wsHandler = s.initWebSocketHandler()

	return nil
}

func (s *Server) initKnowledgeBase(kb interface{}) (*knowledge.KnowledgeBase, error) {
	if kb == nil {
		return nil, nil
	}
	knowledgeBase, ok := kb.(*knowledge.KnowledgeBase)
	if !ok {
		return nil, fmt.Errorf("kb is not *knowledge.KnowledgeBase")
	}
	return knowledgeBase, nil
}

func (s *Server) initWebSocketHub() *websocket.Hub {
	hub := websocket.NewHub()
	go func() {
		defer utils.RecoverWithCustomLogger("Hub.Run", s.logger)
		hub.Run()
	}()
	return hub
}

func (s *Server) initWeatherService() (weather.Service, error) {
	return weather.NewService(weather.Config{
		Provider: s.config.Weather.Provider,
		QWeather: weather.QWeatherConfig{
			APIKey:     s.config.Weather.QWeather.APIKey,
			BaseURL:    s.config.Weather.QWeather.BaseURL,
			Timeout:    s.config.Weather.QWeather.Timeout.Duration,
			MaxRetries: s.config.Weather.QWeather.MaxRetries,
		},
		OpenMeteo: weather.OpenMeteoConfig{
			Timeout:    s.config.Weather.OpenMeteo.Timeout.Duration,
			MaxRetries: s.config.Weather.OpenMeteo.MaxRetries,
		},
	}, s.logger)
}

func (s *Server) initToolRegistry(kb *knowledge.KnowledgeBase, weatherSvc weather.Service) (*tools.ToolRegistry, error) {
	if kb == nil {
		s.logger.Warn("KnowledgeBase is nil, creating empty tool registry")
	}
	return tools.NewToolRegistry(kb, weatherSvc, s.logger)
}

func (s *Server) initLLMAdapters(registry *tools.ToolRegistry) (llm.Adapter, llm.Adapter) {
	primary := llm.NewEinoAgentAdapter(s.logger, s.config.LLM, registry.List())
	fallback := llm.NewFallbackAdapter()
	return primary, fallback
}

func (s *Server) initEmbeddingAPI() cost.EmbeddingAPI {
	if !s.config.Cost.Embedding.Enabled {
		return nil
	}

	if s.config.Cost.Embedding.Type == "local" {
		return cost.NewLocalEmbeddingClientWithConfig(cost.LocalEmbeddingConfig{
			ModelName:  s.config.Cost.Embedding.Model,
			BaseURL:    s.config.Cost.Embedding.BaseURL,
			ServerType: s.config.Cost.Embedding.ServerType,
		})
	}

	if s.config.Cost.Embedding.APIKey == "" {
		s.logger.Warn("Embedding API key not set, semantic caching will be unavailable")
		return nil
	}

	return cost.NewOpenAIEmbeddingClient(
		s.config.Cost.Embedding.APIKey,
		s.config.Cost.Embedding.BaseURL,
		s.config.Cost.Embedding.Model,
	)
}

func (s *Server) initSummarizer(adapter llm.Adapter) cost.Summarizer {
	summaryTimeout := s.config.Cost.SummaryTimeout.Duration
	if summaryTimeout == 0 {
		summaryTimeout = 15 * time.Second
	}

	summaryModel := s.config.Cost.SummaryModel
	if summaryModel == "" {
		for _, mc := range s.config.LLM.Models {
			if mc.Enabled {
				summaryModel = mc.Name
				break
			}
		}
	}

	if summaryModel == "" {
		s.logger.Warn("No model available for summarizer, LLM summarization disabled")
		return nil
	}

	return cost.NewLLMSummarizer(adapter, summaryModel, summaryTimeout, s.logger)
}

func (s *Server) initOptimizer(embedding cost.EmbeddingAPI, summarizer cost.Summarizer) *cost.Optimizer {
	return cost.NewOptimizer(
		s.config.Cost.CacheTTL.Duration,
		s.config.Cost.MaxHistoryMessages,
		s.config.Cost.MaxHistoryTokens,
		embedding,
		summarizer,
		s.logger,
	)
}

func (s *Server) initRuntime(adapter, fallback llm.Adapter, registry *tools.ToolRegistry, optimizer *cost.Optimizer, detector emotion.Detector) *agent.Runtime {
	return agent.NewRuntime(
		adapter,
		fallback,
		registry,
		s.sessionManager,
		optimizer,
		detector,
		agent.Config{
			MaxRetries:       s.config.LLM.MaxRetries,
			Timeout:          s.config.Server.ReadTimeout.Duration,
			LLMTimeout:       s.config.LLM.Timeout.Duration,
			ToolTimeout:      s.config.LLM.Timeout.Duration,
			FallbackResponse: s.config.LLM.FallbackResponse,
		},
		s.logger,
	)
}

func (s *Server) initWebSocketHandler() *WebSocketHandler {
	return NewWebSocketHandler(
		s.agentHub,
		s.sessionManager,
		s.agentRuntime,
		database.NewPlayerRepository(s.db),
		database.NewConversationRepository(s.db),
		s.auditRepo,
		s.logger,
	)
}
