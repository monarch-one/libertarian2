// ANCAP WEB - A Libertarian RSS Reader for the World
// ===================================================
//
// CARACTERÍSTICAS:
// - Sistema de autenticación JWT seguro
// - Lector RSS con cache y paralelización
// - Encriptación end-to-end de contenido
// - Proxy Tor integrado para privacidad
// - Interfaz PWA moderna y responsive
// - Base de datos PostgreSQL escalable
// - API REST completa para apps móviles
//
// CREDENCIALES POR DEFECTO:
// - admin / admin123
// - ancap / ghanima

package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-contrib/cors"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"ancap-web/internal/api"
	"ancap-web/internal/auth"
	"ancap-web/internal/storage"
	"ancap-web/internal/rss"
	"ancap-web/internal/encryption"
	"ancap-web/internal/privacy"
	"ancap-web/pkg/utils"
)

func main() {
	// Configurar logger estructurado
	logger := initLogger()
	defer logger.Sync()

	logger.Info("🚀 Iniciando ANCAP WEB - Lector RSS Libertario para el Mundo")

	// Cargar configuración
	config, err := loadConfig()
	if err != nil {
		logger.Fatal("❌ Error cargando configuración", zap.Error(err))
	}

	// Inicializar base de datos
	db, err := storage.InitDatabase(config.Database)
	if err != nil {
		logger.Fatal("❌ Error inicializando base de datos", zap.Error(err))
	}
	defer db.Close()

	// Inicializar servicios
	authService := auth.NewService(config.JWT.Secret, config.JWT.Expiration)
	rssService := rss.NewService(db, logger)
	encryptionService := encryption.NewService(config.Encryption.Key)
	privacyService := privacy.NewService(config.Privacy)

	// Configurar Gin
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()

	// Middleware de logging
	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	// CORS para PWA
	router.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"*"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept", "Authorization"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}))

	// Middleware de seguridad
	router.Use(securityMiddleware())

	// Servir archivos estáticos para PWA
	router.Static("/static", "./frontend/public")
	router.StaticFile("/manifest.json", "./frontend/public/manifest.json")
	router.StaticFile("/sw.js", "./frontend/public/sw.js")

	// API routes
	api.SetupRoutes(router, api.Services{
		Auth:       authService,
		RSS:        rssService,
		Encryption: encryptionService,
		Privacy:    privacyService,
		Logger:     logger,
	})

	// Frontend PWA
	router.GET("/", func(c *gin.Context) {
		c.File("./frontend/public/index.html")
	})

	// Crear servidor HTTP
	srv := &http.Server{
		Addr:         config.Server.Address,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Iniciar servidor en goroutine
	go func() {
		logger.Info("🌐 Servidor iniciando en", zap.String("address", config.Server.Address))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("❌ Error iniciando servidor", zap.Error(err))
		}
	}()

	// Esperar señal de interrupción
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("🛑 Cerrando servidor...")

	// Contexto con timeout para shutdown graceful
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Fatal("❌ Error cerrando servidor", zap.Error(err))
	}

	logger.Info("✅ Servidor cerrado exitosamente")
}

func initLogger() *zap.Logger {
	config := zap.NewProductionConfig()
	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	config.EncoderConfig.StacktraceKey = ""

	logger, err := config.Build()
	if err != nil {
		log.Fatal("Error inicializando logger:", err)
	}

	return logger
}

func loadConfig() (*Config, error) {
	// TODO: Implementar carga de configuración con Viper
	return &Config{
		Server: ServerConfig{
			Address: ":8080",
		},
		Database: DatabaseConfig{
			Host:     "localhost",
			Port:     5432,
			User:     "ancap",
			Password: "ghanima",
			Name:     "ancap_web",
			SSLMode:  "disable",
		},
		JWT: JWTConfig{
			Secret:     "your-secret-key-change-in-production",
			Expiration: 24 * time.Hour,
		},
		Encryption: EncryptionConfig{
			Key: "your-encryption-key-32-bytes-long",
		},
		Privacy: PrivacyConfig{
			UseTor:       true,
			UseVPN:       false,
			RotateIP:     true,
			ClearHistory: true,
			NoLogs:       false,
		},
	}, nil
}

func securityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Headers de seguridad
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		
		// Prevenir cache de contenido sensible
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate, private")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")

		c.Next()
	}
}

// Config structs
type Config struct {
	Server     ServerConfig     `json:"server"`
	Database   DatabaseConfig   `json:"database"`
	JWT        JWTConfig        `json:"jwt"`
	Encryption EncryptionConfig `json:"encryption"`
	Privacy    PrivacyConfig    `json:"privacy"`
}

type ServerConfig struct {
	Address string `json:"address"`
}

type DatabaseConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Name     string `json:"name"`
	SSLMode  string `json:"ssl_mode"`
}

type JWTConfig struct {
	Secret     string        `json:"secret"`
	Expiration time.Duration `json:"expiration"`
}

type EncryptionConfig struct {
	Key string `json:"key"`
}

type PrivacyConfig struct {
	UseTor       bool `json:"use_tor"`
	UseVPN       bool `json:"use_vpn"`
	RotateIP     bool `json:"rotate_ip"`
	ClearHistory bool `json:"clear_history"`
	NoLogs       bool `json:"no_logs"`
}
