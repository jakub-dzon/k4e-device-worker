package heartbeat_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/project-flotta/flotta-device-worker/internal/ansible"
	os2 "github.com/project-flotta/flotta-device-worker/internal/os"
	"github.com/project-flotta/flotta-device-worker/internal/registration"
	log "github.com/sirupsen/logrus"

	"github.com/golang/mock/gomock"
	"github.com/project-flotta/flotta-device-worker/internal/configuration"
	"github.com/project-flotta/flotta-device-worker/internal/datatransfer"
	"github.com/project-flotta/flotta-device-worker/internal/hardware"
	"github.com/project-flotta/flotta-device-worker/internal/heartbeat"
	"github.com/project-flotta/flotta-device-worker/internal/workload"
	"github.com/project-flotta/flotta-device-worker/internal/workload/api"
	"github.com/project-flotta/flotta-operator/models"
	"google.golang.org/grpc"

	pb "github.com/redhatinsights/yggdrasil/protocol"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Heartbeat", func() {

	var (
		datadir        = "/tmp"
		ansibleDir     = "/tmp/ansible_test"
		mockCtrl       *gomock.Controller
		wkManager      *workload.WorkloadManager
		configManager  *configuration.Manager
		ansibleManager *ansible.Manager
		wkwMock        *workload.MockWorkloadWrapper
		hwMock         *hardware.MockHardware
		monitor        = &datatransfer.Monitor{}
		hb             = &heartbeat.Heartbeat{}
		err            error
		client         Dispatcher
		deviceOs       *os2.OS
	)

	BeforeEach(func() {
		defer GinkgoRecover()
		mockCtrl = gomock.NewController(GinkgoT())
		wkwMock = workload.NewMockWorkloadWrapper(mockCtrl)
		wkwMock.EXPECT().Init().Return(nil).AnyTimes()
		wkwMock.EXPECT().PersistConfiguration().AnyTimes()

		regMock := registration.NewMockRegistrationWrapper(mockCtrl)
		wkManager, err = workload.NewWorkloadManagerWithParams(datadir, wkwMock, "device-id-123")
		Expect(err).NotTo(HaveOccurred(), "Cannot start the Workload Manager")

		hwMock = hardware.NewMockHardware(mockCtrl)

		configManager = configuration.NewConfigurationManager(datadir)

		client = Dispatcher{}
		gracefulRebootChannel := make(chan struct{})
		osExecCommands := os2.NewOsExecCommands()
		deviceOs = os2.NewOS(gracefulRebootChannel, osExecCommands)

		ansibleManager, err = ansible.NewAnsibleManager(&client, ansibleDir)
		Expect(err).NotTo(HaveOccurred(), "Cannot start the Ansible Manager")

		hb = heartbeat.NewHeartbeatService(&client,
			configManager,
			wkManager,
			hwMock,
			monitor,
			deviceOs,
			regMock)
	})

	AfterEach(func() {
		mockCtrl.Finish()
	})

	Context("HeartBeatData test", func() {
		It("Report empty workloads an up status", func() {
			//given
			wkwMock.EXPECT().List().Times(1)
			hbData := heartbeat.NewHeartbeatData(configManager, wkManager, ansibleManager, hwMock, monitor, deviceOs)

			//when
			heartbeatInfo := hbData.RetrieveInfo()

			//then
			Expect(heartbeatInfo.Status).To(Equal("up"))
			Expect(heartbeatInfo.Workloads).To(BeEmpty())
		})

		It("Report workload correctly", func() {
			//given
			hbData := heartbeat.NewHeartbeatData(configManager, wkManager, ansibleManager, hwMock, monitor, deviceOs)

			wkwMock.EXPECT().List().Return([]api.WorkloadInfo{{
				Id:     "test",
				Name:   "test",
				Status: "Running",
			}}, nil).AnyTimes()

			//when
			heartbeatInfo := hbData.RetrieveInfo()

			//then
			Expect(heartbeatInfo.Status).To(Equal("up"))

			// Workload checks
			Expect(heartbeatInfo.Workloads).To(HaveLen(1))
			Expect(heartbeatInfo.Workloads[0].Name).To(Equal("test"))
			Expect(heartbeatInfo.Workloads[0].Status).To(Equal("Running"))
		})

		It("Report ansible events correctly", func() {
			//given
			ansibleErrorMsg := "test playbook error string"
			ansibleEventReason := "Failed"
			ansibleManager.AddToEventQueue(&models.EventInfo{
				Message: ansibleErrorMsg,
				Reason:  ansibleEventReason,
				Type:    models.EventInfoTypeWarn,
			})
			//given
			wkwMock.EXPECT().List().Times(1)
			hbData := heartbeat.NewHeartbeatData(configManager, wkManager, ansibleManager, hwMock, monitor, deviceOs)

			//when
			heartbeatInfo := hbData.RetrieveInfo()

			//then

			// Workload checks
			Expect(heartbeatInfo.Status).To(Equal("up"))
			Expect(heartbeatInfo.Workloads).To(BeEmpty())

			// Ansible checks
			Expect(heartbeatInfo.Events).To(HaveLen(1))
			Expect(heartbeatInfo.Events[0].Message).To(Equal(ansibleErrorMsg))
			Expect(heartbeatInfo.Events[0].Reason).To(Equal(ansibleEventReason))
		})

		It("Cannot retrieve the list of workloads", func() {
			//given
			hbData := heartbeat.NewHeartbeatData(configManager, wkManager, ansibleManager, hwMock, monitor, deviceOs)

			wkwMock.EXPECT().List().Return([]api.WorkloadInfo{}, fmt.Errorf("invalid list")).AnyTimes()

			//when
			heartbeatInfo := hbData.RetrieveInfo()

			//then
			Expect(heartbeatInfo.Status).To(Equal("up"))
			Expect(heartbeatInfo.Workloads).To(HaveLen(0))
		})

		It("Report workload hw delta enable without changes", func() {
			//given
			_, _, createHardwareMutableInformationCall := initHwMock(hwMock, configManager, "localhost", []string{"127.0.0.1", "0.0.0.0"})
			wkwMock.EXPECT().List().Return([]api.WorkloadInfo{{
				Id:     "test",
				Name:   "test",
				Status: "Running",
			}}, nil).AnyTimes()
			hbData := heartbeat.NewHeartbeatData(configManager, wkManager, ansibleManager, hwMock, monitor, deviceOs)
			createHardwareMutableInformationCall.Times(2)
			//when
			heartbeatInfo := hbData.RetrieveInfo()

			//then

			// Hardware checks first time
			Expect(heartbeatInfo.Hardware.CPU).To(Not(BeNil()))
			Expect(heartbeatInfo.Hardware.Hostname).To(Equal("localhost"))
			Expect(heartbeatInfo.Hardware.Interfaces).To(Not(BeNil()))
			Expect(heartbeatInfo.Hardware.SystemVendor).To(Not(BeNil()))

			// Hardware checks delta, no changes
			heartbeatInfo = hbData.RetrieveInfo()
			Expect(heartbeatInfo.Hardware.CPU).To(BeNil())
			Expect(heartbeatInfo.Hardware.Hostname).To(BeEmpty())
			Expect(heartbeatInfo.Hardware.Interfaces).To(BeNil())
			Expect(heartbeatInfo.Hardware.SystemVendor).To(BeNil())
		})

		It("Report workload hw delta enable with hostname change", func() {
			//given
			initHwMock(hwMock, configManager, "localhost", []string{"127.0.0.1", "0.0.0.0"})
			wkwMock.EXPECT().List().Return([]api.WorkloadInfo{{
				Id:     "test",
				Name:   "test",
				Status: "Running",
			}}, nil).AnyTimes()
			hbData := heartbeat.NewHeartbeatData(configManager, wkManager, ansibleManager, hwMock, monitor, deviceOs)
			//when
			hbData.RetrieveInfo()

			// Hardware checks delta, hostname change
			hwMock.EXPECT().CreateHardwareMutableInformation().Return(&models.HardwareInfo{
				Hostname: "localhostNEW",
				Interfaces: []*models.Interface{{
					IPV4Addresses: []string{"127.0.0.1", "0.0.0.0"},
				}},
			}, nil).Times(1)
			heartbeatInfo := hbData.RetrieveInfo()
			Expect(heartbeatInfo.Hardware.CPU).To(BeNil())
			Expect(heartbeatInfo.Hardware.Hostname).To(Equal("localhostNEW"))
			Expect(heartbeatInfo.Hardware.Interfaces).To(BeNil())
			Expect(heartbeatInfo.Hardware.SystemVendor).To(BeNil())
		})

		It("Report workload hw delta enable interface change", func() {
			_, getMutableHardwareInfoDeltaCall, createHardwareMutableInformationCall := initHwMock(hwMock, configManager, "localhost", []string{"127.0.0.1", "0.0.0.0"})
			wkwMock.EXPECT().List().Return([]api.WorkloadInfo{{
				Id:     "test",
				Name:   "test",
				Status: "Running",
			}}, nil).AnyTimes()
			hbData := heartbeat.NewHeartbeatData(configManager, wkManager, ansibleManager, hwMock, monitor, deviceOs)
			getMutableHardwareInfoDeltaCall.Times(2)
			createHardwareMutableInformationCall.Times(2)
			//when
			hbData.RetrieveInfo()
			// get empty delta
			hbData.RetrieveInfo()

			//then
			// Hardware checks delta, interface change
			hwMock.EXPECT().CreateHardwareMutableInformation().Return(&models.HardwareInfo{
				Hostname: "localhost",
				Interfaces: []*models.Interface{{
					IPV4Addresses: []string{"127.0.0.1", "0.0.0.0"},
					IPV6Addresses: []string{"f8:75:a4:a4:00:fe"},
				}},
			}, nil)

			heartbeatInfo := hbData.RetrieveInfo()
			Expect(heartbeatInfo.Hardware.CPU).To(BeNil())
			Expect(heartbeatInfo.Hardware.Hostname).To(BeEmpty())
			Expect(heartbeatInfo.Hardware.Interfaces).To(Not(BeNil()))
			Expect(heartbeatInfo.Hardware.SystemVendor).To(BeNil())
		})

		It("Report workload hw delta enable both hostname and interfaces change", func() {
			//given
			initHwMock(hwMock, configManager, "localhost", []string{"127.0.0.1", "0.0.0.0"})
			wkwMock.EXPECT().List().Return([]api.WorkloadInfo{{
				Id:     "test",
				Name:   "test",
				Status: "Running",
			}}, nil).AnyTimes()
			hbData := heartbeat.NewHeartbeatData(configManager, wkManager, ansibleManager, hwMock, monitor, deviceOs)

			//when
			hbData.RetrieveInfo()

			//then
			// Hardware checks delta, both hostname and interface change
			hwMock.EXPECT().CreateHardwareMutableInformation().Return(&models.HardwareInfo{
				Hostname: "localhostFINAL",
				Interfaces: []*models.Interface{{
					IPV4Addresses: []string{"127.0.0.1", "0.0.0.0", "10.0.0.1"},
					IPV6Addresses: []string{"f8:75:a4:a4:00:fe"},
				}},
			}, nil).Times(1)
			heartbeatInfo := hbData.RetrieveInfo()
			Expect(heartbeatInfo.Hardware.CPU).To(BeNil())
			Expect(heartbeatInfo.Hardware.Hostname).To(Equal("localhostFINAL"))
			Expect(heartbeatInfo.Hardware.Interfaces).To(Not(BeNil()))
			Expect(heartbeatInfo.Hardware.SystemVendor).To(BeNil())

		})

		It("Report workload hw delta disable", func() {
			//given
			hostname := "localhost"
			interfaces := []*models.Interface{{
				IPV4Addresses: []string{"127.0.0.1", "0.0.0.0"},
			}}

			wkwMock.EXPECT().List().Return([]api.WorkloadInfo{{
				Id:     "test",
				Name:   "test",
				Status: "Running",
			}}, nil).AnyTimes()

			hwMock.EXPECT().GetHardwareInformation().Return(&models.HardwareInfo{
				Hostname:     hostname,
				Interfaces:   interfaces,
				CPU:          &models.CPU{Architecture: "TestArchi", ModelName: "ModelTest"},
				SystemVendor: &models.SystemVendor{Manufacturer: "ManufacturerTest", ProductName: "ProductTest", SerialNumber: "SerialTest"},
			}, nil)

			hwMock.EXPECT().CreateHardwareMutableInformation().Return(&models.HardwareInfo{
				Hostname:   hostname,
				Interfaces: interfaces,
			}, nil).AnyTimes()

			configManager.GetDeviceConfiguration().Heartbeat.HardwareProfile.Scope = heartbeat.ScopeFull
			configManager.GetDeviceConfiguration().Heartbeat.HardwareProfile.Include = true
			hbData := heartbeat.NewHeartbeatData(configManager, wkManager, ansibleManager, hwMock, monitor, deviceOs)

			//when
			heartbeatInfo := hbData.RetrieveInfo()

			// then
			// Hardware checks first time
			Expect(heartbeatInfo.Hardware.CPU).To(Not(BeNil()))
			Expect(heartbeatInfo.Hardware.Hostname).To(Equal(hostname))
			Expect(heartbeatInfo.Hardware.Interfaces).To(Not(BeNil()))
			Expect(heartbeatInfo.Hardware.SystemVendor).To(Not(BeNil()))

			// Hardware checks second time: only mutable info
			heartbeatInfo = hbData.RetrieveInfo()
			Expect(heartbeatInfo.Hardware.CPU).To(BeNil())
			Expect(heartbeatInfo.Hardware.Hostname).To(Equal(hostname))
			Expect(heartbeatInfo.Hardware.Interfaces).To(Not(BeNil()))
			Expect(heartbeatInfo.Hardware.SystemVendor).To(BeNil())

		})

		It("Report workload hw info disable", func() {
			//given
			wkwMock.EXPECT().List().Return([]api.WorkloadInfo{{
				Id:     "test",
				Name:   "test",
				Status: "Running",
			}}, nil).AnyTimes()

			configManager.GetDeviceConfiguration().Heartbeat.HardwareProfile.Include = false
			hbData := heartbeat.NewHeartbeatData(configManager, wkManager, ansibleManager, hwMock, monitor, deviceOs)

			//when
			heartbeatInfo := hbData.RetrieveInfo()

			// Hardware checks first time
			Expect(heartbeatInfo.Hardware).To(BeNil())
		})
	})

	Context("Start", func() {
		It("Ticker is stopped if it's not started", func() {
			//given
			Expect(hb.HasStarted()).To(BeFalse(), "Ticker is initialized when it shouldn't")

			// when
			hb.Start()

			//then
			Expect(hb.HasStarted()).To(BeTrue())
		})

		It("Hearbeat is sent with error", func() {
			//given

			wkwMock.EXPECT().List().AnyTimes()
			getHardwareInformationCall, getMutableHardwareInfoDeltaCall, createHardwareMutableInformationCall := initHwMock(hwMock, configManager, "localhost", []string{"127.0.0.1", "0.0.0.0"})

			getHardwareInformationCall.Times(5)
			createHardwareMutableInformationCall.Times(5)
			getMutableHardwareInfoDeltaCall.Times(0)
			clientFail := DispatcherFailing{}
			hb := createCustomHeartbeatWithDispatcher(&clientFail, mockCtrl, datadir, int64(1), wkManager, hwMock, monitor, deviceOs)

			Expect(hb.HasStarted()).To(BeFalse(), "Ticker is initialized when it shouldn't")

			// when
			hb.Start()
			Expect(hb.HasStarted()).To(BeTrue())
			time.Sleep(5 * time.Second)

			Expect(hb.Deregister()).ToNot(HaveOccurred())

			//then
			hwInfoList := clientFail.GetHwInfoList()
			hwFirstInfo := hwInfoList[0]
			Expect(hwFirstInfo.CPU).To(Not(BeNil()))
			Expect(hwFirstInfo.Hostname).To(Equal("localhost"))
			Expect(hwFirstInfo.Interfaces).To(Not(BeNil()))
			Expect(hwFirstInfo.SystemVendor).To(Not(BeNil()))
			for _, hwInfo := range hwInfoList[1:] {
				Expect(reflect.DeepEqual(hwFirstInfo, hwInfo)).To(BeTrue())
			}

		})
		It("Hearbeat is sent without error", func() {
			//given
			_, getMutableHardwareInfoDeltaCall, createHardwareMutableInformationCall := initHwMock(hwMock, configManager, "localhost", []string{"127.0.0.1", "0.0.0.0"})
			wkwMock.EXPECT().List().AnyTimes()

			// min and max times are used because of the sleep, sometimes a tick is missed
			createHardwareMutableInformationCall.MinTimes(4).MaxTimes(5)
			getMutableHardwareInfoDeltaCall.MinTimes(3).MaxTimes(4)
			//have to create ne Dispatcher to avoid race error
			clientSuccess := Dispatcher{}
			hb := createCustomHeartbeatWithDispatcher(&clientSuccess, mockCtrl, datadir, int64(1), wkManager, hwMock, monitor, deviceOs)

			Expect(hb.HasStarted()).To(BeFalse(), "Ticker is initialized when it shouldn't")

			// when
			hb.Start()
			Expect(hb.HasStarted()).To(BeTrue())
			time.Sleep(5 * time.Second)

			Expect(hb.Deregister()).ToNot(HaveOccurred())

			//then
			hwInfoList := clientSuccess.GetHwInfoList()
			hwFirstInfo := hwInfoList[0]
			hwSecondInfo := hwInfoList[1]
			Expect(hwFirstInfo.CPU).To(Not(BeNil()))
			Expect(hwFirstInfo.Hostname).To(Equal("localhost"))
			Expect(hwFirstInfo.Interfaces).To(Not(BeNil()))
			Expect(hwFirstInfo.SystemVendor).To(Not(BeNil()))

			Expect(hwSecondInfo.CPU).To(BeNil())
			Expect(hwSecondInfo.Hostname).To(BeEmpty())
			Expect(hwSecondInfo.Interfaces).To(BeNil())
			Expect(hwSecondInfo.SystemVendor).To(BeNil())
			for _, hwInfo := range hwInfoList[2:] {
				Expect(reflect.DeepEqual(hwSecondInfo, hwInfo)).To(BeTrue())
			}

		})

		It("Hearbeat is sent and empty response is given", func() {
			//given

			wkwMock.EXPECT().List().AnyTimes()
			getHardwareInformationCall, getMutableHardwareInfoDeltaCall, createHardwareMutableInformationCall := initHwMock(hwMock, configManager, "localhost", []string{"127.0.0.1", "0.0.0.0"})

			getHardwareInformationCall.Times(1)
			createHardwareMutableInformationCall.Times(1)
			getMutableHardwareInfoDeltaCall.Times(0)
			clientEmpty := DispatcherEmptyResponse{}
			hb := createCustomHeartbeatWithDispatcher(&clientEmpty, mockCtrl, datadir, 2, wkManager, hwMock, monitor, deviceOs)

			Expect(hb.HasStarted()).To(BeFalse(), "Ticker is initialized when it shouldn't")
			var buf Buffer
			writer := bufio.NewWriter(&buf)
			log.SetOutput(writer)
			log.SetLevel(log.TraceLevel)
			// when
			hb.Start()
			Expect(hb.HasStarted()).To(BeTrue())
			time.Sleep(3 * time.Second)
			Expect(hb.Deregister()).ToNot(HaveOccurred())
			writer.Flush()
			//then
			Expect(buf.String()).To(ContainSubstring("empty response received, host may not be reachable"))

		})
	})

	Context("Update", func() {

		BeforeEach(func() {
			wkwMock.EXPECT().List().AnyTimes()
		})

		It("Ticker is created", func() {

			//given
			Expect(hb.HasStarted()).To(BeFalse(), "Ticker is initialized when it shouldn't")

			cfg := models.DeviceConfigurationMessage{
				Configuration: &models.DeviceConfiguration{Heartbeat: &models.HeartbeatConfiguration{PeriodSeconds: 1}},
				DeviceID:      "",
				Version:       "",
				Workloads:     []*models.Workload{},
			}

			// when
			err := hb.Update(cfg)

			// then
			Expect(err).NotTo(HaveOccurred())
			Expect(hb.HasStarted()).To(BeTrue())
			Expect(hb.Deregister()).ToNot(HaveOccurred())
		})
		It("Ticker not created on invalid config", func() {

			// given
			Expect(hb.HasStarted()).To(BeFalse(), "Ticker is initialized when it shouldn't")

			cfg := models.DeviceConfigurationMessage{
				Configuration: &models.DeviceConfiguration{},
				DeviceID:      "",
				Version:       "",
				Workloads:     []*models.Workload{},
			}

			// when
			err := hb.Update(cfg)

			// then
			Expect(err).NotTo(HaveOccurred())
			Expect(hb.HasStarted()).To(BeTrue())
			Expect(hb.Deregister()).ToNot(HaveOccurred())
		})

		It("Ticker is updated with new PeriodSeconds", func() {
			defer GinkgoRecover()
			//given
			initialPeriod := 2
			newPeriod := 3
			_, getMutableHardwareInfoDeltaCall, createHardwareMutableInformationCall := initHwMock(hwMock, configManager, "localhost", []string{"127.0.0.1", "0.0.0.0"})
			wkwMock.EXPECT().List().AnyTimes()

			createHardwareMutableInformationCall.AnyTimes()
			getMutableHardwareInfoDeltaCall.AnyTimes()
			//have to create ne Dispatcher to avoid race error
			clientSuccess := Dispatcher{}
			hb := createCustomHeartbeatWithDispatcher(&clientSuccess, mockCtrl, datadir, int64(initialPeriod), wkManager, hwMock, monitor, deviceOs)
			Expect(hb.HasStarted()).To(BeFalse(), "Ticker is initialized when it shouldn't")
			var buf Buffer
			writer := bufio.NewWriter(&buf)
			log.SetOutput(writer)
			log.SetLevel(log.TraceLevel)
			// when
			hb.Start()
			Expect(hb.HasStarted()).To(BeTrue())
			time.Sleep(5 * time.Second)

			//then
			Expect(len(clientSuccess.GetHwInfoList())).To(Equal(2))
			clientSuccess.ClearHwInfoList()
			// given
			cfg := models.DeviceConfigurationMessage{
				Configuration: &models.DeviceConfiguration{Heartbeat: &models.HeartbeatConfiguration{PeriodSeconds: int64(newPeriod)}},
				DeviceID:      "",
				Version:       "",
				Workloads:     []*models.Workload{},
			}

			// when
			// Calling Update in goroutines to simulate and test data races
			quit_1 := make(chan bool)
			go func() {
				for {
					select {
					case <-quit_1:
						return
					default:
						Expect(hb.Update(cfg)).NotTo(HaveOccurred())
					}
				}
			}()
			quit_2 := make(chan bool)
			go func() {
				for {
					select {
					case <-quit_2:
						return
					default:
						Expect(hb.Update(cfg)).NotTo(HaveOccurred())
					}
				}
			}()
			err := hb.Update(cfg)

			// then
			Expect(err).NotTo(HaveOccurred())
			Expect(hb.HasStarted()).To(BeTrue())
			time.Sleep(5 * time.Second)

			Expect(len(clientSuccess.GetHwInfoList())).To(Equal(1))
			quit_1 <- true
			quit_2 <- true
			Expect(buf.String()).To(ContainSubstring(fmt.Sprintf("Heartbeat configuration update: periodSeconds changed from %d to %d", initialPeriod, newPeriod)))

			Expect(hb.Deregister()).ToNot(HaveOccurred())
		})

		It("Ticker is NOT updated as period is the same", func() {
			//given
			initialPeriod := 2
			_, getMutableHardwareInfoDeltaCall, createHardwareMutableInformationCall := initHwMock(hwMock, configManager, "localhost", []string{"127.0.0.1", "0.0.0.0"})
			wkwMock.EXPECT().List().AnyTimes()

			createHardwareMutableInformationCall.AnyTimes()
			getMutableHardwareInfoDeltaCall.AnyTimes()
			//have to create ne Dispatcher to avoid race error
			clientSuccess := Dispatcher{}
			hb := createCustomHeartbeatWithDispatcher(&clientSuccess, mockCtrl, datadir, int64(initialPeriod), wkManager, hwMock, monitor, deviceOs)

			Expect(hb.HasStarted()).To(BeFalse(), "Ticker is initialized when it shouldn't")
			var buf Buffer
			writer := bufio.NewWriter(&buf)
			log.SetOutput(writer)
			// when
			hb.Start()
			Expect(hb.HasStarted()).To(BeTrue())
			time.Sleep(5 * time.Second)

			//then
			Expect(len(clientSuccess.GetHwInfoList())).To(Equal(2))

			// given
			cfg := models.DeviceConfigurationMessage{
				Configuration: &models.DeviceConfiguration{Heartbeat: &models.HeartbeatConfiguration{PeriodSeconds: int64(initialPeriod)}},
				DeviceID:      "",
				Version:       "",
				Workloads:     []*models.Workload{},
			}

			// when
			err := hb.Update(cfg)

			// then
			Expect(err).NotTo(HaveOccurred())
			Expect(hb.HasStarted()).To(BeTrue())
			time.Sleep(6 * time.Second)
			// should be 5 at the end of the test: the ticker was not stopped and that the tests lasted 11 seconds with a 2 sec periods so 5 HWInfo in total
			Expect(len(clientSuccess.GetHwInfoList())).To(Equal(5))
			Expect(buf.String()).To(Not(ContainSubstring("Heartbeat configuration update: periodSeconds changed from")))

			Expect(hb.Deregister()).ToNot(HaveOccurred())

		})

	})

})

// We keep the latest send data to make sure that we validate the data sent to
// the operator without sent at all
type Dispatcher struct {
	latestData *pb.Data
	hwInfoList []*models.HardwareInfo
	lock       sync.Mutex
}

func (d *Dispatcher) GetHwInfoList() []*models.HardwareInfo {
	d.lock.Lock()
	defer d.lock.Unlock()
	cpy := make([]*models.HardwareInfo, len(d.hwInfoList))
	copy(cpy, d.hwInfoList)
	return cpy
}

func (d *Dispatcher) ClearHwInfoList() {
	d.lock.Lock()
	defer d.lock.Unlock()
	d.hwInfoList = make([]*models.HardwareInfo, 0)
}

func (d *Dispatcher) Send(ctx context.Context, in *pb.Data, opts ...grpc.CallOption) (*pb.Response, error) {
	d.lock.Lock()
	defer d.lock.Unlock()
	heartbeat := models.Heartbeat{}
	err := json.Unmarshal(in.Content, &heartbeat)
	if err != nil {
		return nil, err
	}
	response := &pb.Response{}
	yggResponse := registration.YGGDResponse{}
	yggResponse.StatusCode = http.StatusOK
	response.Response, err = json.Marshal(yggResponse)
	if err != nil {
		return nil, err
	}

	d.latestData = in
	d.hwInfoList = append(d.hwInfoList, heartbeat.Hardware)

	return response, nil
}

func (d *Dispatcher) Register(ctx context.Context, in *pb.RegistrationRequest, opts ...grpc.CallOption) (*pb.RegistrationResponse, error) {
	return nil, nil
}

func (d *Dispatcher) GetConfig(ctx context.Context, in *pb.Empty, opts ...grpc.CallOption) (*pb.Config, error) {
	return nil, nil
}

type DispatcherEmptyResponse struct {
	latestData *pb.Data
	hwInfoList []*models.HardwareInfo
	lock       sync.Mutex
}

func (d *DispatcherEmptyResponse) GetHwInfoList() []*models.HardwareInfo {
	d.lock.Lock()
	defer d.lock.Unlock()
	cpy := make([]*models.HardwareInfo, len(d.hwInfoList))
	copy(cpy, d.hwInfoList)
	return cpy
}
func (d *DispatcherEmptyResponse) Send(ctx context.Context, in *pb.Data, opts ...grpc.CallOption) (*pb.Response, error) {
	d.lock.Lock()
	defer d.lock.Unlock()
	heartbeat := models.Heartbeat{}
	err := json.Unmarshal(in.Content, &heartbeat)
	if err != nil {
		return nil, err
	}

	d.latestData = in
	d.hwInfoList = append(d.hwInfoList, heartbeat.Hardware)

	return &pb.Response{}, nil
}

func (d *DispatcherEmptyResponse) Register(ctx context.Context, in *pb.RegistrationRequest, opts ...grpc.CallOption) (*pb.RegistrationResponse, error) {
	return nil, nil
}

func (d *DispatcherEmptyResponse) GetConfig(ctx context.Context, in *pb.Empty, opts ...grpc.CallOption) (*pb.Config, error) {
	return nil, nil
}

type DispatcherFailing struct {
	latestData *pb.Data
	hwInfoList []*models.HardwareInfo
	lock       sync.Mutex
}

func (d *DispatcherFailing) GetHwInfoList() []*models.HardwareInfo {
	d.lock.Lock()
	defer d.lock.Unlock()
	cpy := make([]*models.HardwareInfo, len(d.hwInfoList))
	copy(cpy, d.hwInfoList)
	return cpy
}
func (d *DispatcherFailing) Send(ctx context.Context, in *pb.Data, opts ...grpc.CallOption) (*pb.Response, error) {
	d.lock.Lock()
	defer d.lock.Unlock()
	heartbeat := models.Heartbeat{}
	err := json.Unmarshal(in.Content, &heartbeat)
	if err != nil {
		return nil, err
	}

	d.hwInfoList = append(d.hwInfoList, heartbeat.Hardware)
	d.latestData = in

	return nil, fmt.Errorf("Error sending")
}

func (d *DispatcherFailing) Register(ctx context.Context, in *pb.RegistrationRequest, opts ...grpc.CallOption) (*pb.RegistrationResponse, error) {
	return nil, nil
}

func (d *DispatcherFailing) GetConfig(ctx context.Context, in *pb.Empty, opts ...grpc.CallOption) (*pb.Config, error) {
	return nil, nil
}

func initHwMock(hwMock *hardware.MockHardware, configManager *configuration.Manager, hostname string, ipv4 []string) (*gomock.Call, *gomock.Call, *gomock.Call) {
	var m models.HardwareInfo
	configManager.GetDeviceConfiguration().Heartbeat.HardwareProfile.Scope = heartbeat.ScopeDelta
	configManager.GetDeviceConfiguration().Heartbeat.HardwareProfile.Include = true

	getHardwareInformationCall := hwMock.EXPECT().GetHardwareInformation().Return(&models.HardwareInfo{
		Hostname: hostname,
		Interfaces: []*models.Interface{{
			IPV4Addresses: ipv4,
		}},
		CPU:          &models.CPU{Architecture: "TestArchi", ModelName: "ModelTest"},
		SystemVendor: &models.SystemVendor{Manufacturer: "ManufacturerTest", ProductName: "ProductTest", SerialNumber: "SerialTest"},
	}, nil)

	getMutableHardwareInfoDeltaCall := hwMock.EXPECT().GetMutableHardwareInfoDelta(gomock.AssignableToTypeOf(m), gomock.AssignableToTypeOf(m)).DoAndReturn(
		func(hardwareMutableInfoPrevious models.HardwareInfo, hardwareMutableInfoNew models.HardwareInfo) *models.HardwareInfo {
			return hardware.GetMutableHardwareInfoDelta(hardwareMutableInfoPrevious, hardwareMutableInfoNew)
		})
	createHardwareMutableInformationCall := hwMock.EXPECT().CreateHardwareMutableInformation().Return(&models.HardwareInfo{
		Hostname: hostname,
		Interfaces: []*models.Interface{{
			IPV4Addresses: ipv4,
		}},
	}, nil)

	return getHardwareInformationCall, getMutableHardwareInfoDeltaCall, createHardwareMutableInformationCall
}

func createCustomHeartbeatWithDispatcher(client pb.DispatcherClient, mockCtrl *gomock.Controller, datadir string, periodSeconds int64, wkManager *workload.WorkloadManager, hwMock *hardware.MockHardware, monitor *datatransfer.Monitor, deviceOs *os2.OS) *heartbeat.Heartbeat {
	regMock := registration.NewMockRegistrationWrapper(mockCtrl)
	configManager := configuration.NewConfigurationManager(datadir)
	configManager.GetDeviceConfiguration().Heartbeat.PeriodSeconds = periodSeconds
	configManager.GetDeviceConfiguration().Heartbeat.HardwareProfile.Scope = heartbeat.ScopeDelta
	configManager.GetDeviceConfiguration().Heartbeat.HardwareProfile.Include = true
	return heartbeat.NewHeartbeatService(client,
		configManager,
		wkManager,
		hwMock,
		monitor,
		deviceOs,
		regMock)
}

type Buffer struct {
	b bytes.Buffer
	m sync.Mutex
}

func (b *Buffer) Read(p []byte) (n int, err error) {
	b.m.Lock()
	defer b.m.Unlock()
	return b.b.Read(p)
}
func (b *Buffer) Write(p []byte) (n int, err error) {
	b.m.Lock()
	defer b.m.Unlock()
	return b.b.Write(p)
}
func (b *Buffer) String() string {
	b.m.Lock()
	defer b.m.Unlock()
	return b.b.String()
}
