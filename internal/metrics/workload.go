package metrics

import (
	"fmt"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/project-flotta/flotta-device-worker/internal/workload/podman"
	"github.com/project-flotta/flotta-operator/models"
)

const (
	defaultInterval int32 = 60
)

type WorkloadMetrics struct {
	daemon         MetricsDaemon
	workloadConfig map[string]*models.Workload
	lock           sync.RWMutex
}

func NewWorkloadMetrics(daemon MetricsDaemon) *WorkloadMetrics {
	return &WorkloadMetrics{daemon: daemon}
}

func (wrkM *WorkloadMetrics) getWorkload(workloadName string) *models.Workload {
	wrkM.lock.Lock()
	defer wrkM.lock.Unlock()
	return wrkM.workloadConfig[workloadName]
}

func (wrkM *WorkloadMetrics) Init(config models.DeviceConfigurationMessage) error {
	return wrkM.Update(config)
}

func (wrkM *WorkloadMetrics) Update(config models.DeviceConfigurationMessage) error {
	cfg := map[string]*models.Workload{}
	for _, workload := range config.Workloads {
		cfg[workload.Name] = workload
	}

	wrkM.lock.Lock()
	defer wrkM.lock.Unlock()
	wrkM.workloadConfig = cfg
	return nil
}

func (wrkM *WorkloadMetrics) WorkloadRemoved(workloadName string) {
	log.Infof("Removing target metrics for workload '%v'", workloadName)
	wrkM.daemon.DeleteTarget(workloadName)
}

func (wrkM *WorkloadMetrics) WorkloadStarted(workloadName string, report []*podman.PodReport) {
	log.Infof("Starting target metrics for workload '%s'", workloadName)
	for _, workload := range report {
		cfg := wrkM.getWorkload(workloadName)
		if cfg == nil {
			log.Infof("workload '%v' started but it's not part of config", workloadName)
			continue
		}

		if cfg.Metrics == nil {
			continue
		}

		var filter SampleFilter = &PermissiveAllowList{}
		if cfg.Metrics.AllowList != nil {
			filter = NewRestrictiveAllowList(cfg.Metrics.AllowList)
		}

		urls := []string{}
		for _, workloadReport := range report {
			urls = append(urls, getWorkloadUrls(workloadReport, cfg)...)
		}

		interval := defaultInterval
		if cfg.Metrics.Interval > 0 {
			interval = cfg.Metrics.Interval
		}
		// log for this is part of the AddTarget function
		wrkM.daemon.AddTarget(workload.Name, CreateHTTPScraper(urls), time.Duration(interval)*time.Second, filter)
	}
}

func (wrKM *WorkloadMetrics) String() string {
	return "workload metrics"
}

func getWorkloadUrls(report *podman.PodReport, config *models.Workload) []string {
	res := []string{}
	metricsPath := config.Metrics.Path
	port := config.Metrics.Port
	for _, container := range report.Containers {
		if customConfig, ok := config.Metrics.Containers[container.Name]; ok {
			if customConfig.Disabled {
				continue
			}
			res = append(res,
				fmt.Sprintf("http://%s:%d%s",
					container.IPAddress, customConfig.Port,
					getPathOrDefault(customConfig.Path)))
		} else {
			res = append(res,
				fmt.Sprintf("http://%s:%d%s",
					container.IPAddress, port,
					getPathOrDefault(metricsPath)))
		}
	}
	return res
}

func getPathOrDefault(path string) string {
	if path == "" {
		return "/"
	}
	return path
}
