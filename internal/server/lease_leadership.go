package server

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// LeaseLeader manages Kubernetes Lease-based leadership election.
// Only the leader runs the credential probe loop; without leadership,
// the server serves traffic but probes NOTHING (fail closed).
type LeaseLeader struct {
	mu              sync.RWMutex
	leaseName       string
	leaseNamespace  string
	identity        string
	client          *kubernetes.Clientset
	leaseDuration   time.Duration
	renewDeadline   time.Duration
	retryPeriod     time.Duration
	leaderElected   bool
	heldLease       *coordinationv1.Lease
	stopCh          chan struct{}
	stopped         bool
	onLeadershipLost func()
}

// LeaseConfig holds the configuration for Lease-based leader election.
type LeaseConfig struct {
	// LeaseName is the name of the Lease resource (e.g., "seam-credential-probe")
	LeaseName string

	// LeaseNamespace is the namespace of the Lease resource (e.g., "seam")
	LeaseNamespace string

	// LeaseDuration is the duration of the lease (e.g., 15s)
	LeaseDuration time.Duration

	// RenewDeadline is the deadline for renewing the lease (e.g., 10s)
	RenewDeadline time.Duration

	// RetryPeriod is the retry period for acquiring/renewing the lease (e.g., 2s)
	RetryPeriod time.Duration

	// OnLeadershipLost is called when leadership is lost
	OnLeadershipLost func()
}

// NewLeaseLeader creates a new Kubernetes Lease leader elector.
// If not in a Kubernetes environment, returns a leader that immediately
// acquires leadership (for local development).
func NewLeaseLeader(cfg LeaseConfig) (*LeaseLeader, error) {
	if cfg.LeaseName == "" {
		return nil, fmt.Errorf("lease name is required")
	}
	if cfg.LeaseNamespace == "" {
		cfg.LeaseNamespace = "seam"
	}
	if cfg.LeaseDuration == 0 {
		cfg.LeaseDuration = 15 * time.Second
	}
	if cfg.RenewDeadline == 0 {
		cfg.RenewDeadline = 10 * time.Second
	}
	if cfg.RetryPeriod == 0 {
		cfg.RetryPeriod = 2 * time.Second
	}

	// Generate a unique identity for this instance
	identity := hostname()

	// Try to create in-cluster Kubernetes client
	client, err := inClusterClient()
	if err != nil {
		// Not in cluster - return a leader that immediately succeeds (local dev)
		if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
			return &LeaseLeader{
				leaseName:       cfg.LeaseName,
				leaseNamespace:  cfg.LeaseNamespace,
				identity:        identity,
				leaseDuration:   cfg.LeaseDuration,
				renewDeadline:   cfg.RenewDeadline,
				retryPeriod:     cfg.RetryPeriod,
				leaderElected:   true, // Local dev: always "leader"
				stopCh:          make(chan struct{}),
				onLeadershipLost: cfg.OnLeadershipLost,
			}, nil
		}
		return nil, fmt.Errorf("create in-cluster client: %w", err)
	}

	return &LeaseLeader{
		leaseName:       cfg.LeaseName,
		leaseNamespace:  cfg.LeaseNamespace,
		identity:        identity,
		client:          client,
		leaseDuration:   cfg.LeaseDuration,
		renewDeadline:   cfg.RenewDeadline,
		retryPeriod:     cfg.RetryPeriod,
		stopCh:          make(chan struct{}),
		onLeadershipLost: cfg.OnLeadershipLost,
	}, nil
}

// Acquire attempts to acquire leadership via Kubernetes Lease.
// It blocks until leadership is acquired, the context is cancelled, or Stop is called.
// Returns true if leadership was acquired, false if the attempt was abandoned.
func (l *LeaseLeader) Acquire(ctx context.Context) bool {
	if l == nil {
		return false
	}

	// Local dev mode: already "leader"
	l.mu.Lock()
	if l.leaderElected {
		l.mu.Unlock()
		return true
	}
	l.mu.Unlock()

	if l.client == nil {
		// No client means local dev - succeed immediately
		l.mu.Lock()
		l.leaderElected = true
		l.mu.Unlock()
		return true
	}

	ticker := time.NewTicker(l.retryPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-l.stopCh:
			return false
		case <-ticker.C:
			if l.tryAcquireOrRenew(ctx) {
				l.mu.Lock()
				l.leaderElected = true
				l.mu.Unlock()
				return true
			}
		}
	}
}

// tryAcquireOrRenew attempts to acquire or renew the lease.
// Returns true if successful (became leader or renewed successfully).
func (l *LeaseLeader) tryAcquireOrRenew(ctx context.Context) bool {
	if l == nil || l.client == nil {
		return false
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.stopped {
		return false
	}

	now := metav1.NowMicro()
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      l.leaseName,
			Namespace: l.leaseNamespace,
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &l.identity,
			LeaseDurationSeconds: func() *int32 { d := int32(l.leaseDuration.Seconds()); return &d }(),
			RenewTime:            &now,
			LeaseTransitions:     new(int32),
			AcquireTime:          &now,
		},
	}

	// Try to create or update the lease
	existing, err := l.client.CoordinationV1().Leases(l.leaseNamespace).Get(ctx, l.leaseName, metav1.GetOptions{})
	if err == nil {
		// Lease exists - try to update if we're the holder
		if existing.Spec.HolderIdentity != nil && *existing.Spec.HolderIdentity == l.identity {
			// We're the holder - renew
			existing.Spec.RenewTime = &now
			_, err = l.client.CoordinationV1().Leases(l.leaseNamespace).Update(ctx, existing, metav1.UpdateOptions{})
			if err == nil {
				l.heldLease = existing
				return true
			}
		}
		// Someone else holds the lease
		return false
	}

	// Lease doesn't exist - try to create
	_, err = l.client.CoordinationV1().Leases(l.leaseNamespace).Create(ctx, lease, metav1.CreateOptions{})
	if err == nil {
		l.heldLease = lease
		return true
	}

	return false
}

// Renew periodically renews the lease while leadership is held.
// Blocks until leadership is lost, context is cancelled, or Stop is called.
// Returns when leadership is lost.
func (l *LeaseLeader) Renew(ctx context.Context) {
	if l == nil {
		return
	}

	// Local dev mode: never lose leadership
	l.mu.RLock()
	if l.leaderElected && l.client == nil {
		l.mu.RUnlock()
		<-ctx.Done()
		return
	}
	l.mu.RUnlock()

	ticker := time.NewTicker(l.retryPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-l.stopCh:
			return
		case <-ticker.C:
			if !l.tryAcquireOrRenew(ctx) {
				// Failed to renew - leadership lost
				l.mu.Lock()
				l.leaderElected = false
				l.mu.Unlock()

				// Release the lease
				l.release(ctx)

				// Notify callback if set
				if l.onLeadershipLost != nil {
					l.onLeadershipLost()
				}
				return
			}
		}
	}
}

// Release releases the lease if held.
func (l *LeaseLeader) Release(ctx context.Context) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.stopped {
		return
	}

	l.release(ctx)
}

func (l *LeaseLeader) release(ctx context.Context) {
	if l.client == nil || l.heldLease == nil {
		return
	}

	// Try to delete the lease
	_ = l.client.CoordinationV1().Leases(l.leaseNamespace).Delete(ctx, l.leaseName, metav1.DeleteOptions{})
	l.heldLease = nil
}

// IsLeader reports whether this instance currently holds leadership.
func (l *LeaseLeader) IsLeader() bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.leaderElected
}

// Stop stops the leader election loop and releases the lease if held.
func (l *LeaseLeader) Stop() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.stopped {
		return
	}

	l.stopped = true
	close(l.stopCh)

	// Release leadership
	ctx := context.Background()
	l.release(ctx)
}

// hostname returns the hostname for leader identity.
func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "seam-" + rand.String(5)
	}
	return h
}

// inClusterClient creates a Kubernetes client from in-cluster config.
func inClusterClient() (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

// CheckInCluster detects if running in a Kubernetes cluster.
func CheckInCluster() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}
