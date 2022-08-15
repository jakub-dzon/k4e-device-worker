package workload

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/hashicorp/go-multierror"
	"github.com/project-flotta/flotta-device-worker/internal/mount"
	"github.com/project-flotta/flotta-device-worker/internal/service"
	"github.com/project-flotta/flotta-device-worker/internal/volumes"

	"git.sr.ht/~spc/go-log"
	api2 "github.com/project-flotta/flotta-device-worker/internal/workload/api"
	"github.com/project-flotta/flotta-operator/models"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

const (
	defaultWorkloadsMonitoringInterval = 15

	AuthFileName     = "auth.json"
	WorkloadFileName = "workload.yaml"
)

type WorkloadManager struct {
	workloadsDir   string
	volumesDir     string
	workloads      WorkloadWrapper
	managementLock sync.Locker
	deregistered   bool
	eventsQueue    []*models.EventInfo
	deviceId       string
}

func NewWorkloadManager(dataDir string, deviceId string, systemdEventCh <-chan *service.Event) (*WorkloadManager, error) {
	wrapper, err := newWorkloadInstance(dataDir, defaultWorkloadsMonitoringInterval, systemdEventCh)
	if err != nil {
		return nil, err
	}

	return NewWorkloadManagerWithParams(dataDir, wrapper, deviceId)
}

func NewWorkloadManagerWithParams(dataDir string, ww WorkloadWrapper, deviceId string) (*WorkloadManager, error) {
	return NewWorkloadManagerWithParamsAndInterval(dataDir, ww, defaultWorkloadsMonitoringInterval, deviceId)
}

func NewWorkloadManagerWithParamsAndInterval(dataDir string, ww WorkloadWrapper, monitorInterval uint, deviceId string) (*WorkloadManager, error) {
	workloadsDir := path.Join(dataDir, "workloads")
	/* #nosec */
	if err := os.MkdirAll(workloadsDir, 0777); err != nil {
		return nil, fmt.Errorf("cannot create directory: %w", err)
	}
	volumesDir := path.Join(dataDir, "volumes")

	/* #nosec */
	if err := os.MkdirAll(volumesDir, 0777); err != nil {
		return nil, fmt.Errorf("cannot create directory: %w", err)
	}
	manager := WorkloadManager{
		workloadsDir:   workloadsDir,
		volumesDir:     volumesDir,
		workloads:      ww,
		managementLock: &sync.Mutex{},
		deregistered:   false,
		deviceId:       deviceId,
	}
	if err := manager.workloads.Init(); err != nil {
		return nil, err
	}

	return &manager, nil
}

func (w *WorkloadManager) String() string {
	return "workload manager"
}

// PopEvents return copy of all the events stored in eventQueue
func (w *WorkloadManager) PopEvents() []*models.EventInfo {
	w.managementLock.Lock()
	defer w.managementLock.Unlock()

	// Copy the events:
	events := []*models.EventInfo{}
	for _, event := range w.eventsQueue {
		e := *event
		events = append(events, &e)
	}
	// Empty the events:
	w.eventsQueue = []*models.EventInfo{}
	return events
}

func (w *WorkloadManager) ListWorkloads() ([]api2.WorkloadInfo, error) {
	return w.workloads.List()
}

func (w *WorkloadManager) GetExportedHostPath(workloadName string) string {
	return volumes.HostPathVolumePath(w.volumesDir, workloadName)
}

func (w *WorkloadManager) GetDeviceID() string {
	return w.deviceId
}

func (w *WorkloadManager) Init(configuration models.DeviceConfigurationMessage) error {
	return w.Update(configuration)
}

func (w *WorkloadManager) Update(configuration models.DeviceConfigurationMessage) error {
	w.managementLock.Lock()
	defer w.managementLock.Unlock()
	var errors error
	if w.deregistered {
		log.Infof("deregistration was finished, no need to update anymore. DeviceID: %s", w.deviceId)
		return errors
	}

	errs := w.updateSecrets(configuration.Secrets)
	if len(errs) != 0 {
		errors = multierror.Append(errors, errs...)
	}

	configuredWorkloadNameSet := make(map[string]struct{})
	for _, workload := range configuration.Workloads {
		log.Tracef("deploying workload: %s. DeviceID: %s;", workload.Name, w.deviceId)
		configuredWorkloadNameSet[workload.Name] = struct{}{}

		// Temporal hack to extract podman annotations from the list of annotation propagated to the pod metadata
		// This needs to be reimplemented so that the workload contains a field for annotations to the pod and another one to podman

		pod, err := w.toPod(workload)
		if err != nil {
			errors = multierror.Append(errors, fmt.Errorf(
				"cannot convert workload '%s' to pod. DeviceID: %s; err: %v", workload.Name, err, w.deviceId))
			continue
		}

		if PodShouldWaitForMount(pod, configuration.Configuration) {
			errors = multierror.Append(errors, fmt.Errorf(
				"pod '%s' needs to mount blockdevice but it's not in there yet", workload.Name))
			continue
		}

		if err := w.ensureWorkloadDirExists(pod.Name); err != nil {
			errors = multierror.Append(errors, fmt.Errorf(
				"cannot create workload directory for workload '%s': %s", workload.Name, err))
			continue
		}

		podYaml, err := w.toPodYaml(pod, workload.Configmaps)
		if err != nil {
			errors = multierror.Append(errors, fmt.Errorf("cannot create pod's Yaml. DeviceID: %s; err:  %v", w.deviceId, err))
			continue
		}

		var authFile string
		if workload.ImageRegistries != nil {
			authFile = workload.ImageRegistries.AuthFile
		}

		manifestPath := w.getManifestPath(pod.Name)
		authFilePath := w.getAuthFilePath(pod.Name)
		if !w.podConfigurationModified(manifestPath, podYaml, authFilePath, authFile) {
			log.Tracef("pod '%s' definition is unchanged (%s). DeviceID: %s;", workload.Name, manifestPath, w.deviceId)
			continue
		}
		err = w.storeFile(manifestPath, podYaml)
		if err != nil {
			errors = multierror.Append(errors, fmt.Errorf(
				"cannot store manifest for workload '%s': %s", workload.Name, err))
			continue
		}

		authFilePath, err = w.manageAuthFile(authFilePath, authFile)
		if err != nil {
			errors = multierror.Append(errors, fmt.Errorf(
				"cannot store auth configuration for workload '%s': %s", workload.Name, err))
			continue
		}

		err = w.workloads.Remove(workload.Name)
		if err != nil {
			log.Errorf("error removing workload %s. DeviceID: %s; err: %v", workload.Name, w.deviceId, err)
			errors = multierror.Append(errors, fmt.Errorf("error removing workload %s: %s", workload.Name, err))
			continue
		}

		// TODO: extract podman specific annotations from the workload.
		err = w.workloads.Run(pod, manifestPath, authFilePath, workload.Annotations)
		if err != nil {
			log.Errorf("cannot run workload. DeviceID: %s; err: %v", w.deviceId, err)
			w.eventsQueue = append(w.eventsQueue, &models.EventInfo{
				Message: err.Error(),
				Reason:  "Failed",
				Type:    models.EventInfoTypeWarn,
			})

			errors = multierror.Append(errors, fmt.Errorf(
				"cannot run workload '%s': %s", workload.Name, err))
			continue
		}
	}

	deployedWorkloadByName, err := w.indexWorkloads()
	if err != nil {
		log.Errorf("cannot get deployed workloads. DeviceID: %s; err: %v", w.deviceId, err)
		errors = multierror.Append(errors, fmt.Errorf("cannot get deployed workloads: %s", err))
		return errors
	}
	// Remove any workloads that don't correspond to the configured ones
	for name := range deployedWorkloadByName {
		if _, ok := configuredWorkloadNameSet[name]; !ok {
			log.Infof("workload not found: %s. Removing. DeviceID: %s;", name, w.deviceId)
			if err := deleteDir(w.getWorkloadDirPath(name)); err != nil {
				errors = multierror.Append(errors, fmt.Errorf("cannot remove existing workload directory: %s", err))
			}
			if err := w.workloads.Remove(name); err != nil {
				errors = multierror.Append(errors, fmt.Errorf("cannot remove stale workload name='%s': %s", name, err))
			}
			log.Infof("workload %s removed. DeviceID: %s;", name, w.deviceId)
		}
	}

	return errors
}

func (w *WorkloadManager) Logs(podID string, res io.Writer) (context.CancelFunc, error) {
	return w.workloads.Logs(podID, res)
}

func (w *WorkloadManager) ensureWorkloadDirExists(workloadName string) error {
	workloadDirPath := w.getWorkloadDirPath(workloadName)
	if _, err := os.Stat(workloadDirPath); err != nil {
		/* #nosec */
		if err := os.MkdirAll(workloadDirPath, 0755); err != nil {
			return err
		}
	}
	return nil
}

// manageAuthFile is responsible for bringing auth configuration file under authFilePath to expected state;
// if the content of the file - authFile is supposed to be blank, the file is removed, otherwise authFile is written
// to the authFilePath file.
func (w *WorkloadManager) manageAuthFile(authFilePath, authFile string) (string, error) {
	if authFile == "" {
		if err := deleteFile(authFilePath); err != nil {
			return "", fmt.Errorf("cannot remove auth file %s: %s", authFilePath, err)
		}
		return "", nil
	}
	if err := w.storeFile(authFilePath, []byte(authFile)); err != nil {
		return "", fmt.Errorf("cannot store auth file %s: %s", authFilePath, err)
	}
	return authFilePath, nil
}

func (w *WorkloadManager) storeFile(filePath string, content []byte) error {
	return ioutil.WriteFile(filePath, content, 0600)
}

func (w *WorkloadManager) getAuthFilePath(workloadName string) string {
	return path.Join(w.getWorkloadDirPath(workloadName), AuthFileName)
}

func (w *WorkloadManager) getWorkloadDirPath(workloadName string) string {
	return path.Join(w.workloadsDir, strings.ReplaceAll(workloadName, " ", "-"))
}

func (w *WorkloadManager) getManifestPath(workloadName string) string {
	return path.Join(w.getWorkloadDirPath(workloadName), WorkloadFileName)
}

func (w *WorkloadManager) toPodYaml(pod *v1.Pod, configmaps models.ConfigmapList) ([]byte, error) {
	podYaml, err := yaml.Marshal(pod)
	if err != nil {
		return nil, err
	}

	cmYaml := ""
	if len(configmaps) > 0 {
		cmYaml = strings.Join(configmaps, "---\n")
	}

	return []byte(strings.Join([]string{string(podYaml), string(cmYaml)}, "---\n")), nil
}

func (w *WorkloadManager) indexWorkloads() (map[string]api2.WorkloadInfo, error) {
	workloads, err := w.workloads.List()
	if err != nil {
		return nil, err
	}
	nameToWorkload := make(map[string]api2.WorkloadInfo)
	for _, workload := range workloads {
		nameToWorkload[workload.Name] = workload
	}
	return nameToWorkload, nil
}

func (w *WorkloadManager) RegisterObserver(observer Observer) {
	w.workloads.RegisterObserver(observer)
}

func (w *WorkloadManager) StopWorkloads() error {
	w.managementLock.Lock()
	defer w.managementLock.Unlock()

	err := w.stopAllWorkloads()
	if err != nil {
		log.Errorf("failed to remove workloads. DeviceID: %s; err: %v", w.deviceId, err)
		return err
	}

	return nil
}

func (w *WorkloadManager) Deregister() error {
	w.managementLock.Lock()
	defer w.managementLock.Unlock()

	var errors error
	err := w.removeAllWorkloads()
	if err != nil {
		errors = multierror.Append(errors, fmt.Errorf("failed to remove workloads: %v", err))
		log.Errorf("failed to remove workloads. DeviceID: %s; err: %v", w.deviceId, err)
	}

	err = w.deleteWorkloadsDir()
	if err != nil {
		errors = multierror.Append(errors, fmt.Errorf("failed to delete manifests directory: %v", err))
		log.Errorf("failed to delete manifests directory. DeviceID: %s; err: %v", w.deviceId, err)
	}

	err = w.deleteTable()
	if err != nil {
		errors = multierror.Append(errors, fmt.Errorf("failed to delete table: %v", err))
		log.Errorf("failed to delete table. DeviceID: %s; err: %v", w.deviceId, err)
	}

	err = w.deleteVolumeDir()
	if err != nil {
		errors = multierror.Append(errors, fmt.Errorf("failed to delete volumes directory: %v", err))
		log.Errorf("failed to delete volumes directory. DeviceID: %s; err: %v", w.deviceId, err)
	}

	w.deregistered = true
	return errors
}

func (w *WorkloadManager) removeAllWorkloads() error {
	log.Infof("removing all workload.  DeviceID: %s;", w.deviceId)
	workloads, err := w.workloads.List()
	if err != nil {
		return err
	}

	var res error
	for _, workload := range workloads {
		log.Infof("removing workload %s.  DeviceID: %s;", workload.Name, w.deviceId)
		err := w.workloads.Remove(workload.Name)
		if err != nil {
			log.Errorf("error removing workload %s. DeviceID: %s; err: %v", workload.Name, w.deviceId, err)
			res = multierror.Append(res, err)
		}
	}
	return res
}
func (w *WorkloadManager) stopAllWorkloads() error {
	log.Infof("stopping all workloads. DeviceID: %s;", w.deviceId)
	workloads, err := w.workloads.List()
	if err != nil {
		return err
	}
	var res error
	for _, workload := range workloads {
		log.Infof("stopping workload %s. DeviceID: %s;", workload.Name, w.deviceId)
		err := w.workloads.Stop(workload.Name)
		if err != nil {
			log.Errorf("error stopping workload %s. DeviceID: %s; err: %v", workload.Name, w.deviceId, err)
			res = multierror.Append(res, err)
		}
	}
	return res
}

func (w *WorkloadManager) deleteWorkloadsDir() error {
	log.Infof("deleting manifests directory. DeviceID: %s;", w.deviceId)
	return deleteDir(w.workloadsDir)
}

func (w *WorkloadManager) deleteVolumeDir() error {
	log.Infof("deleting volumes directory. DeviceID: %s;", w.deviceId)
	return deleteDir(w.volumesDir)
}

func (w *WorkloadManager) ListenServiceEvents() {
	w.workloads.ListenServiceEvents()
}

func deleteDir(path string) error {
	err := os.RemoveAll(path)
	if err != nil {
		log.Error(err)
		return err
	}

	return nil
}

func deleteFile(file string) error {
	if err := os.Remove(file); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (w *WorkloadManager) deleteTable() error {
	log.Infof("deleting nftable. DeviceID: %s;", w.deviceId)
	err := w.workloads.RemoveTable()
	if err != nil {
		log.Error(err)
		return err
	}

	return nil
}

func (w *WorkloadManager) toPod(workload *models.Workload) (*v1.Pod, error) {
	podSpec := v1.PodSpec{}
	err := yaml.Unmarshal([]byte(workload.Specification), &podSpec)
	if err != nil {
		return nil, err
	}
	pod := v1.Pod{
		Spec: podSpec,
	}
	pod.Kind = "Pod"
	pod.Name = workload.Name
	pod.Annotations = workload.Annotations
	pod.Labels = workload.Labels
	exportVolume := volumes.HostPathVolume(w.volumesDir, workload.Name)
	pod.Spec.Volumes = append(pod.Spec.Volumes, exportVolume)
	var containers []v1.Container
	for _, container := range pod.Spec.Containers {
		mount := v1.VolumeMount{
			Name:      exportVolume.Name,
			MountPath: "/export",
		}
		container.VolumeMounts = append(container.VolumeMounts, mount)
		container.Env = append(container.Env, v1.EnvVar{Name: "DEVICE_ID", Value: w.deviceId})
		containers = append(containers, container)
	}
	pod.Spec.Containers = containers
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}

	// Set the authfile label to pod, if ImageRegistry authfile is set:
	if workload.ImageRegistries != nil && workload.ImageRegistries.AuthFile != "" {
		pod.Labels["io.containers.autoupdate.authfile"] = w.getAuthFilePath(workload.Name)
	}

	// We need to set the PODMAN_SYSTEMD_UNIT label manually here, because when pod is started via
	// podman play kube command it doesn't have any CreateCommand, which is needed to re-run the pod
	// when new systemd file is genereted, which is responsible for setting the label based on env
	// variable.
	for label := range pod.Labels {
		if ok := strings.Contains(label, "io.containers.autoupdate"); ok {
			pod.Labels["PODMAN_SYSTEMD_UNIT"] = service.DefaultServiceName(workload.Name)
		}
	}

	return &pod, nil
}

func PodShouldWaitForMount(pod *v1.Pod, deviceConf *models.DeviceConfiguration) bool {
	if deviceConf == nil || len(deviceConf.Mounts) == 0 {
		return false
	}

	paths := make(map[string]struct{})
	for _, mountSpec := range deviceConf.Mounts {
		if mountSpec != nil {
			paths[mountSpec.Directory] = struct{}{}
		}
	}

	if len(pod.Spec.Volumes) == 0 {
		return false
	}

	for _, volume := range pod.Spec.Volumes {
		if volume.HostPath == nil {
			continue
		}
		if volume.HostPath.Type == nil {
			continue
		}
		// CharDevice is not needed at all because are not mounted, only
		// BlockDevices
		if *volume.HostPath.Type == v1.HostPathBlockDev {
			if _, found := paths[volume.HostPath.Path]; found {
				if !mount.IsPathMounted(volume.HostPath.Path) {
					return true
				}
			}
		}
	}
	return false
}

func (w *WorkloadManager) podConfigurationModified(manifestPath string, podYaml []byte, authPath string, auth string) bool {
	return w.podModified(manifestPath, podYaml) || w.podAuthModified(authPath, auth)
}

func (w *WorkloadManager) podModified(manifestPath string, podYaml []byte) bool {
	file, err := ioutil.ReadFile(manifestPath) //#nosec
	if err != nil {
		return true
	}
	return !bytes.Equal(file, podYaml)
}

func (w *WorkloadManager) podAuthModified(authPath string, auth string) bool {
	if _, err := os.Stat(authPath); err != nil {
		return auth != ""
	}
	file, err := ioutil.ReadFile(authPath) //#nosec
	if err != nil {
		return true
	}
	return !bytes.Equal(file, []byte(auth))
}

func (w *WorkloadManager) updateSecrets(configSecrets models.SecretList) []error {
	deviceSecrets, err := w.workloads.ListSecrets()
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, configSecret := range configSecrets {
		if _, ok := deviceSecrets[configSecret.Name]; ok {
			err = w.workloads.UpdateSecret(configSecret.Name, configSecret.Data)
			delete(deviceSecrets, configSecret.Name)
		} else {
			err = w.workloads.CreateSecret(configSecret.Name, configSecret.Data)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	for deviceName := range deviceSecrets {
		err = w.workloads.RemoveSecret(deviceName)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
