package main

import (
	"context"
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
	defer func() { _ = logger.Sync() }()

	zap.ReplaceGlobals(logger)
	zap.L().Info("Starting SEAM Retirement Evaluator")

	// Load configuration
	cfg, err := loadConfig()
	if err != nil {
		zap.L().Fatal("Failed to load configuration", zap.Error(err))
	}

	// Initialize components. The evaluator is detection-only: it needs
	// VictoriaMetrics read access and nothing else — no git host, no
	// third-party credential.
	metrics := newRetirementMetrics()
	victoriaMetricsClient := NewVictoriaMetricsClient(cfg.VictoriaMetricsEndpoint)
	evaluator := NewRetirementEvaluator(victoriaMetricsClient, cfg, metrics)

	// Start HTTP server for health/ready probes and the metrics surface
	go startHealthServer(metrics)

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

// startHealthServer starts an HTTP server for health/ready probes and the
// evaluator's own Prometheus surface
func startHealthServer(metrics *retirementMetrics) {
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	http.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Ready"))
	})

	http.Handle("/metrics", metrics)

	addr := getEnv("LISTEN_ADDRESS", ":8080")
	// This server carries the only output a detection-only evaluator has, so a
	// bind failure has to be loud rather than a silent loss of the metric.
	if err := http.ListenAndServe(addr, nil); err != nil {
		zap.L().Fatal("Health server failed", zap.String("address", addr), zap.Error(err))
	}
}

// Config holds the evaluator configuration
type Config struct {
	VictoriaMetricsEndpoint string
	DeclarativeConfigPath   string
}

func loadConfig() (*Config, error) {
	cfg := &Config{
		VictoriaMetricsEndpoint: getEnv("VICTORIAMETRICS_ENDPOINT", "http://victorialogs-single-ardenone-manager-vector-headless.monitoring.svc.cluster.local:8428"),
		DeclarativeConfigPath:   getEnv("DECLARATIVE_CONFIG_PATH", "k8s/rs-manager/seam/routes.d"),
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
