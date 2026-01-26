// Package k8s provides Kubernetes service discovery for InfraLens.
// It watches Pods and Services to resolve IP addresses to names.
package k8s

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// Watcher watches Kubernetes Pods and Services to resolve IPs to names.
type Watcher struct {
	client    kubernetes.Interface
	factory   informers.SharedInformerFactory
	podCache  map[string]string // IP -> "Pod: namespace/name"
	svcCache  map[string]string // ClusterIP -> "Service: namespace/name"
	mu        sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
	isEnabled bool
}

// NewWatcher creates a new Kubernetes watcher.
// It attempts to use in-cluster config first, then falls back to KUBECONFIG.
// If neither works, it returns a no-op watcher that always returns the original IP.
func NewWatcher() *Watcher {
	ctx, cancel := context.WithCancel(context.Background())
	w := &Watcher{
		podCache:  make(map[string]string),
		svcCache:  make(map[string]string),
		ctx:       ctx,
		cancel:    cancel,
		isEnabled: false,
	}

	// Try to create Kubernetes client
	config, err := getKubeConfig()
	if err != nil {
		log.WithError(err).Warn("Kubernetes client not available, service discovery disabled")
		return w
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.WithError(err).Warn("Failed to create Kubernetes client, service discovery disabled")
		return w
	}

	// Test connection
	_, err = client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		log.WithError(err).Warn("Failed to connect to Kubernetes API, service discovery disabled")
		return w
	}

	w.client = client
	w.isEnabled = true
	log.Info("Kubernetes service discovery enabled")

	return w
}

// getKubeConfig returns Kubernetes client configuration.
// It tries in-cluster config first, then KUBECONFIG, then default kubeconfig path.
func getKubeConfig() (*rest.Config, error) {
	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err == nil {
		log.Debug("Using in-cluster Kubernetes config")
		return config, nil
	}

	// Try KUBECONFIG environment variable
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		// Try default kubeconfig path
		home, err := os.UserHomeDir()
		if err == nil {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
	}

	if kubeconfig != "" {
		if _, err := os.Stat(kubeconfig); err == nil {
			config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
			if err == nil {
				log.WithField("path", kubeconfig).Debug("Using kubeconfig file")
				return config, nil
			}
		}
	}

	return nil, fmt.Errorf("no valid Kubernetes configuration found")
}

// Start begins watching Kubernetes resources.
func (w *Watcher) Start() {
	if !w.isEnabled {
		log.Info("Kubernetes watcher not started (not connected to cluster)")
		return
	}

	// Create shared informer factory that watches all namespaces
	w.factory = informers.NewSharedInformerFactory(w.client, 30*time.Second)

	// Setup Pod informer
	podInformer := w.factory.Core().V1().Pods().Informer()
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    w.onPodAdd,
		UpdateFunc: w.onPodUpdate,
		DeleteFunc: w.onPodDelete,
	})

	// Setup Service informer
	svcInformer := w.factory.Core().V1().Services().Informer()
	svcInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    w.onServiceAdd,
		UpdateFunc: w.onServiceUpdate,
		DeleteFunc: w.onServiceDelete,
	})

	// Start the informers
	w.factory.Start(w.ctx.Done())

	// Wait for cache sync
	log.Info("Waiting for Kubernetes informer caches to sync...")
	synced := w.factory.WaitForCacheSync(w.ctx.Done())
	for resourceType, ok := range synced {
		if !ok {
			log.WithField("resource", resourceType).Warn("Failed to sync cache")
		}
	}
	log.Info("Kubernetes informer caches synced")

	// Log initial cache stats
	w.mu.RLock()
	log.WithFields(log.Fields{
		"pods":     len(w.podCache),
		"services": len(w.svcCache),
	}).Info("Initial Kubernetes cache populated")
	w.mu.RUnlock()
}

// Stop stops the watcher.
func (w *Watcher) Stop() {
	w.cancel()
	if w.factory != nil {
		w.factory.Shutdown()
	}
	log.Info("Kubernetes watcher stopped")
}

// Resolve attempts to resolve an IP address to a Kubernetes resource name.
// Returns "Pod: namespace/name" or "Service: namespace/name" if found.
// Returns the original IP if not found or if watcher is disabled.
func (w *Watcher) Resolve(ip string) string {
	if !w.isEnabled || ip == "" {
		return ip
	}

	w.mu.RLock()
	defer w.mu.RUnlock()

	// Check Service cache first (ClusterIPs are more stable)
	if name, ok := w.svcCache[ip]; ok {
		return name
	}

	// Check Pod cache
	if name, ok := w.podCache[ip]; ok {
		return name
	}

	return ip
}

// ResolveWithFallback resolves an IP and returns both the resolved name and the original IP.
func (w *Watcher) ResolveWithFallback(ip string) (resolved string, original string) {
	return w.Resolve(ip), ip
}

// IsEnabled returns whether the watcher is connected to Kubernetes.
func (w *Watcher) IsEnabled() bool {
	return w.isEnabled
}

// Stats returns current cache statistics.
func (w *Watcher) Stats() map[string]int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return map[string]int{
		"pods":     len(w.podCache),
		"services": len(w.svcCache),
	}
}

// Pod event handlers

func (w *Watcher) onPodAdd(obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}
	w.updatePodCache(pod)
}

func (w *Watcher) onPodUpdate(oldObj, newObj interface{}) {
	pod, ok := newObj.(*corev1.Pod)
	if !ok {
		return
	}
	w.updatePodCache(pod)
}

func (w *Watcher) onPodDelete(obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		// Handle DeletedFinalStateUnknown
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		pod, ok = tombstone.Obj.(*corev1.Pod)
		if !ok {
			return
		}
	}
	w.removePodFromCache(pod)
}

func (w *Watcher) updatePodCache(pod *corev1.Pod) {
	if pod.Status.PodIP == "" {
		return
	}

	name := fmt.Sprintf("Pod: %s/%s", pod.Namespace, pod.Name)

	w.mu.Lock()
	w.podCache[pod.Status.PodIP] = name

	// Also cache Pod IPs from status.PodIPs (for dual-stack)
	for _, pip := range pod.Status.PodIPs {
		if pip.IP != "" {
			w.podCache[pip.IP] = name
		}
	}
	w.mu.Unlock()

	log.WithFields(log.Fields{
		"pod": name,
		"ip":  pod.Status.PodIP,
	}).Debug("Pod added to cache")
}

func (w *Watcher) removePodFromCache(pod *corev1.Pod) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if pod.Status.PodIP != "" {
		delete(w.podCache, pod.Status.PodIP)
	}

	for _, pip := range pod.Status.PodIPs {
		if pip.IP != "" {
			delete(w.podCache, pip.IP)
		}
	}

	log.WithFields(log.Fields{
		"pod":       fmt.Sprintf("%s/%s", pod.Namespace, pod.Name),
		"ip":        pod.Status.PodIP,
	}).Debug("Pod removed from cache")
}

// Service event handlers

func (w *Watcher) onServiceAdd(obj interface{}) {
	svc, ok := obj.(*corev1.Service)
	if !ok {
		return
	}
	w.updateServiceCache(svc)
}

func (w *Watcher) onServiceUpdate(oldObj, newObj interface{}) {
	svc, ok := newObj.(*corev1.Service)
	if !ok {
		return
	}
	w.updateServiceCache(svc)
}

func (w *Watcher) onServiceDelete(obj interface{}) {
	svc, ok := obj.(*corev1.Service)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		svc, ok = tombstone.Obj.(*corev1.Service)
		if !ok {
			return
		}
	}
	w.removeServiceFromCache(svc)
}

func (w *Watcher) updateServiceCache(svc *corev1.Service) {
	// Skip headless services (ClusterIP = "None")
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == "None" {
		return
	}

	name := fmt.Sprintf("Svc: %s/%s", svc.Namespace, svc.Name)

	w.mu.Lock()
	w.svcCache[svc.Spec.ClusterIP] = name

	// Also cache ClusterIPs for dual-stack services
	for _, ip := range svc.Spec.ClusterIPs {
		if ip != "" && ip != "None" {
			w.svcCache[ip] = name
		}
	}

	// Cache ExternalIPs
	for _, ip := range svc.Spec.ExternalIPs {
		if ip != "" {
			w.svcCache[ip] = name
		}
	}

	// Cache LoadBalancer IPs
	for _, ingress := range svc.Status.LoadBalancer.Ingress {
		if ingress.IP != "" {
			w.svcCache[ingress.IP] = name
		}
	}
	w.mu.Unlock()

	log.WithFields(log.Fields{
		"service":   name,
		"clusterIP": svc.Spec.ClusterIP,
	}).Debug("Service added to cache")
}

func (w *Watcher) removeServiceFromCache(svc *corev1.Service) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if svc.Spec.ClusterIP != "" && svc.Spec.ClusterIP != "None" {
		delete(w.svcCache, svc.Spec.ClusterIP)
	}

	for _, ip := range svc.Spec.ClusterIPs {
		delete(w.svcCache, ip)
	}

	for _, ip := range svc.Spec.ExternalIPs {
		delete(w.svcCache, ip)
	}

	for _, ingress := range svc.Status.LoadBalancer.Ingress {
		if ingress.IP != "" {
			delete(w.svcCache, ingress.IP)
		}
	}

	log.WithFields(log.Fields{
		"service":   fmt.Sprintf("%s/%s", svc.Namespace, svc.Name),
		"clusterIP": svc.Spec.ClusterIP,
	}).Debug("Service removed from cache")
}
