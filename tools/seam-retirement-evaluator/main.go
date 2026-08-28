package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"go.uber.org/zap"
)

const (
	// WindowFloorDays is the minimum retirement evaluation window (7 days)
	WindowFloorDays = 7

	// MaxGapMultiplier is the multiplier for observed max gap (3x)
	MaxGapMultiplier = 3

	// EvaluationInterval is how often to run retirement evaluation
	EvaluationInterval = 1 * time.Hour

	// MinimumHistoryRequired is the minimum history needed to trust the gap calculation
	MinimumHistoryRequired = 14 * 24 * time.Hour // 14 days
)

func main() {
	// Initialize logger
	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Sync()

	zap.ReplaceGlobals(logger)
	zap.L().Info("Starting SEAM Retirement Evaluator")

	// Load configuration
	cfg, err := loadConfig()
	if err != nil {
		zap.L().Fatal("Failed to load configuration", zap.Error(err))
	}

	// Initialize components
	victoriaMetricsClient := NewVictoriaMetricsClient(cfg.VictoriaMetricsEndpoint)
	githubClient := NewGitHubClient(cfg.GitHubToken, cfg.GitHubOwner, cfg.GitHubRepo)
	evaluator := NewRetirementEvaluator(victoriaMetricsClient, githubClient, cfg)

	// Start HTTP server for health/ready probes
	go startHealthServer()

	// Run evaluation loop
	ctx := context.Background()
	ticker := time.NewTicker(EvaluationInterval)
	defer ticker.Stop()

	// Run initial evaluation immediately
	if err := evaluator.RunEvaluation(ctx); err != nil {
		zap.L().Error("Initial evaluation failed", zap.Error(err))
	}

	// Continue with periodic evaluations
	for range ticker.C {
		if err := evaluator.RunEvaluation(ctx); err != nil {
			zap.L().Error("Evaluation failed", zap.Error(err))
		} else {
			zap.L().Info("Evaluation completed successfully")
		}
	}
}

// startHealthServer starts an HTTP server for health/ready probes
func startHealthServer() {
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	http.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Ready"))
	})

	if err := http.ListenAndServe(":8080", nil); err != nil {
		zap.L().Fatal("Health server failed", zap.Error(err))
	}
}

// Config holds the evaluator configuration
type Config struct {
	VictoriaMetricsEndpoint string
	GitHubOwner             string
	GitHubRepo              string
	GitHubToken             string
	DeclarativeConfigPath   string
}

func loadConfig() (*Config, error) {
	cfg := &Config{
		VictoriaMetricsEndpoint: getEnv("VICTORIAMETRICS_ENDPOINT", "http://victorialogs-single-ardenone-manager-vector-headless.monitoring.svc.cluster.local:8428"),
		GitHubOwner:             getEnv("GITHUB_OWNER", "jedarden"),
		GitHubRepo:              getEnv("GITHUB_REPO", "declarative-config"),
		GitHubToken:             getEnv("GITHUB_TOKEN", ""),
		DeclarativeConfigPath:   getEnv("DECLARATIVE_CONFIG_PATH", "k8s/rs-manager/seam/routes.d"),
	}

	if cfg.GitHubToken == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN environment variable is required")
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
