// Copyright 2021 Aleksei Zakharov, https://alexzzz.ru/
// Copyright 2017 Kumina, https://kumina.nl/
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Project forked from https://github.com/kumina/libvirt_exporter
// And then forked from https://github.com/rumanzo/libvirt_exporter_improved

package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AlexZzz/libvirt-exporter/libvirtSchema"
	_ "github.com/go-sql-driver/mysql"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/alecthomas/kingpin.v2"
	"libvirt.org/go/libvirt"
)

// DiskInfo represents information about a disk
type DiskInfo struct {
	Serial        string `json:"serial"`
	BusType       string `json:"bus-type"`
	Bus           int    `json:"bus"`
	Unit          int    `json:"unit"`
	PCIController struct {
		Bus      int `json:"bus"`
		Slot     int `json:"slot"`
		Domain   int `json:"domain"`
		Function int `json:"function"`
	} `json:"pci-controller"`
	Dev    string `json:"dev"`
	Target int    `json:"target"`
}

// PartitionInfo represents information about a partition
type PartitionInfo struct {
	Name       string     `json:"name"`
	TotalBytes int        `json:"total-bytes"`
	Mountpoint string     `json:"mountpoint"`
	Disk       []DiskInfo `json:"disk"`
	UsedBytes  int        `json:"used-bytes"`
	Type       string     `json:"type"`
}

// ReturnData represents the top-level JSON structure
type ReturnData struct {
	Return []PartitionInfo `json:"return"`
}

var confValue map[string]interface{}
var byteValue []uint8
var domainMetaInfo = map[int]map[string]string{}
var networkMetaInfo = map[int]map[string]string{}
var diskMetaInfo = map[int]map[string]string{}
var excludedDomainNames = map[string]struct{}{}
var password = ""
var webTimeoutDuration = 10 * time.Second
var qemuAgentTimeoutDuration = 1 * time.Second
var domStatsTimeoutDuration = 2 * time.Second
var fsInfoIntervalDuration = 0 * time.Second
var enableStatsPerf = false
var enableStatsVcpu = false
var enableDomainBlockIoTune = false
var moldDbConnectTimeoutDuration = 3 * time.Second
var moldMetaIntervalDuration = 120 * time.Second
var moldMetaLastRefresh time.Time
var moldMetaLastStatus = 0.0
var moldMetaMutex sync.RWMutex

type fsInfoMetric struct {
	partitionName       string
	partitionMountpoint string
	partitionType       string
	serial              string
	totalBytes          float64
	usedBytes           float64
}

type fsInfoCacheEntry struct {
	updatedAt time.Time
	status    float64
	metrics   []fsInfoMetric
}

var fsInfoCache = map[string]fsInfoCacheEntry{}
var fsInfoCacheMutex sync.RWMutex

type domainStatsResult struct {
	stats []libvirt.DomainStats
	err   error
}

type AESCipher struct {
	block cipher.Block
}

var (
	libvirtUpDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "", "up"),
		"Whether scraping libvirt's metrics was successful.",
		nil,
		nil)
	libvirtMoldCollectDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "mold_collect", "up"),
		"Whether scraping mold meta  metrics was successful.",
		nil,
		nil)
	libvirtVersionsInfoDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "", "versions_info"),
		"Versions of virtualization components",
		[]string{"hypervisor_running", "libvirtd_running", "libvirt_library"},
		nil)
	libvirtDomainInfoMetaDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_info", "meta"),
		"Domain metadata",
		[]string{"domain", "uuid", "instance_name", "flavor", "user_name", "user_uuid", "project_name", "project_uuid", "root_type", "root_uuid", "domain_mold_name", "vm_user_name", "display_mold_name"},
		nil)
	libvirtDomainInfoMaxMemBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_info", "maximum_memory_bytes"),
		"Maximum allowed memory of the domain, in bytes.",
		[]string{"domain"},
		nil)
	libvirtDomainInfoMemoryUsageBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_info", "memory_usage_bytes"),
		"Memory usage of the domain, in bytes.",
		[]string{"domain"},
		nil)
	libvirtDomainInfoNrVirtCPUDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_info", "virtual_cpus"),
		"Number of virtual CPUs for the domain.",
		[]string{"domain"},
		nil)
	libvirtDomainInfoCPUTimeDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_info", "cpu_time_seconds_total"),
		"Amount of CPU time used by the domain, in seconds.",
		[]string{"domain"},
		nil)
	libvirtDomainInfoVirDomainState = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_info", "vstate"),
		"Virtual domain state. 0: no state, 1: the domain is running, 2: the domain is blocked on resource,"+
			" 3: the domain is paused by user, 4: the domain is being shut down, 5: the domain is shut off,"+
			"6: the domain is crashed, 7: the domain is suspended by guest power management",
		[]string{"domain"},
		nil)

	libvirtDomainVcpuTimeDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_vcpu", "time_seconds_total"),
		"Amount of CPU time used by the domain's VCPU, in seconds.",
		[]string{"domain", "vcpu"},
		nil)
	libvirtDomainVcpuDelayDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_vcpu", "delay_seconds_total"),
		"Amount of CPU time used by the domain's VCPU, in seconds. "+
			"Vcpu's delay metric. Time the vcpu thread was enqueued by the "+
			"host scheduler, but was waiting in the queue instead of running. "+
			"Exposed to the VM as a steal time.",
		[]string{"domain", "vcpu"},
		nil)
	libvirtDomainVcpuStateDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_vcpu", "state"),
		"VCPU state. 0: offline, 1: running, 2: blocked",
		[]string{"domain", "vcpu"},
		nil)
	libvirtDomainVcpuCPUDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_vcpu", "cpu"),
		"Real CPU number, or one of the values from virVcpuHostCpuState",
		[]string{"domain", "vcpu"},
		nil)
	libvirtDomainVcpuWaitDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_vcpu", "wait_seconds_total"),
		"Vcpu's wait_sum metric. CONFIG_SCHEDSTATS has to be enabled",
		[]string{"domain", "vcpu"},
		nil)

	libvirtDomainMetaBlockDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block", "meta"),
		"Block device metadata info. Device name, source file, serial.",
		[]string{"domain", "target_device", "source_file", "serial", "bus", "disk_type", "driver_type", "cache", "discard", "mold_disk_name", "display_volume_name", "mold_volume_type"},
		nil)
	libvirtDomainBlockRdBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "read_bytes_total"),
		"Number of bytes read from a block device, in bytes.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockRdReqDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "read_requests_total"),
		"Number of read requests from a block device.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockRdTotalTimeSecondsDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "read_time_seconds_total"),
		"Total time spent on reads from a block device, in seconds.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockWrBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "write_bytes_total"),
		"Number of bytes written to a block device, in bytes.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockWrReqDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "write_requests_total"),
		"Number of write requests to a block device.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockWrTotalTimesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "write_time_seconds_total"),
		"Total time spent on writes on a block device, in seconds",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockFlushReqDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "flush_requests_total"),
		"Total flush requests from a block device.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockFlushTotalTimeSecondsDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "flush_time_seconds_total"),
		"Total time in seconds spent on cache flushing to a block device",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockAllocationDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "allocation"),
		"Offset of the highest written sector on a block device.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockCapacityBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "capacity_bytes"),
		"Logical size in bytes of the block device	backing image.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockPhysicalSizeBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "physicalsize_bytes"),
		"Physical size in bytes of the container of the backing image.",
		[]string{"domain", "target_device"},
		nil)

	libvirtDomainFsInfoAgentStatusDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_fs_info", "agent_status"),
		"Check agent operation status.",
		[]string{"domain"},
		nil)
	libvirtDomainFsInfoTotalBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_fs_info", "total_bytes"),
		"Total disk capacity of virtual machine mount path.",
		[]string{"domain", "partition_name", "partition_mountpoint", "partition_type", "serial"},
		nil)
	libvirtDomainFsInfoUsageBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_fs_info", "usage_bytes"),
		"Total disk usage of virtual machine mount path",
		[]string{"domain", "partition_name", "partition_mountpoint", "partition_type", "serial"},
		nil)

	// Block IO tune parameters
	// Limits
	libvirtDomainBlockTotalBytesSecDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_total_bytes"),
		"Total throughput limit in bytes per second",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockWriteBytesSecDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_write_bytes"),
		"Write throughput limit in bytes per second",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockReadBytesSecDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_read_bytes"),
		"Read throughput limit in bytes per second",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockTotalIopsSecDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_total_requests"),
		"Total requests per second limit",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockWriteIopsSecDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_write_requests"),
		"Write requests per second limit",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockReadIopsSecDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_read_requests"),
		"Read requests per second limit",
		[]string{"domain", "target_device"},
		nil)
	// Burst limits
	libvirtDomainBlockTotalBytesSecMaxDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_burst_total_bytes"),
		"Total throughput burst limit in bytes per second",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockWriteBytesSecMaxDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_burst_write_bytes"),
		"Write throughput burst limit in bytes per second",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockReadBytesSecMaxDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_burst_read_bytes"),
		"Read throughput burst limit in bytes per second",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockTotalIopsSecMaxDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_burst_total_requests"),
		"Total requests per second burst limit",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockWriteIopsSecMaxDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_burst_write_requests"),
		"Write requests per second burst limit",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockReadIopsSecMaxDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_burst_read_requests"),
		"Read requests per second burst limit",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockTotalBytesSecMaxLengthDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_burst_total_bytes_length_seconds"),
		"Total throughput burst time in seconds",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockWriteBytesSecMaxLengthDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_burst_write_bytes_length_seconds"),
		"Write throughput burst time in seconds",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockReadBytesSecMaxLengthDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_burst_read_bytes_length_seconds"),
		"Read throughput burst time in seconds",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockTotalIopsSecMaxLengthDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_burst_length_total_requests_seconds"),
		"Total requests per second burst time in seconds",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockWriteIopsSecMaxLengthDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_burst_length_write_requests_seconds"),
		"Write requests per second burst time in seconds",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockReadIopsSecMaxLengthDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "limit_burst_length_read_requests_seconds"),
		"Read requests per second burst time in seconds",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainBlockSizeIopsSecDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_block_stats", "size_iops_bytes"),
		"The size of IO operations per second permitted through a block device",
		[]string{"domain", "target_device"},
		nil)

	libvirtDomainMetaInterfacesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_interface", "meta"),
		"Interfaces metadata. Source bridge, target device, interface uuid",
		[]string{"domain", "source_bridge", "target_device", "virtual_interface", "mac_address", "mold_network_name"},
		nil)
	libvirtDomainInterfaceRxBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_interface_stats", "receive_bytes_total"),
		"Number of bytes received on a network interface, in bytes.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainInterfaceRxPacketsDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_interface_stats", "receive_packets_total"),
		"Number of packets received on a network interface.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainInterfaceRxErrsDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_interface_stats", "receive_errors_total"),
		"Number of packet receive errors on a network interface.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainInterfaceRxDropDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_interface_stats", "receive_drops_total"),
		"Number of packet receive drops on a network interface.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainInterfaceTxBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_interface_stats", "transmit_bytes_total"),
		"Number of bytes transmitted on a network interface, in bytes.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainInterfaceTxPacketsDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_interface_stats", "transmit_packets_total"),
		"Number of packets transmitted on a network interface.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainInterfaceTxErrsDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_interface_stats", "transmit_errors_total"),
		"Number of packet transmit errors on a network interface.",
		[]string{"domain", "target_device"},
		nil)
	libvirtDomainInterfaceTxDropDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_interface_stats", "transmit_drops_total"),
		"Number of packet transmit drops on a network interface.",
		[]string{"domain", "target_device"},
		nil)

	libvirtDomainMemoryStatMajorFaultTotalDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_memory_stats", "major_fault_total"),
		"Page faults occur when a process makes a valid access to virtual memory that is not available. "+
			"When servicing the page fault, if disk IO is required, it is considered a major fault.",
		[]string{"domain"},
		nil)
	libvirtDomainMemoryStatMinorFaultTotalDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_memory_stats", "minor_fault_total"),
		"Page faults occur when a process makes a valid access to virtual memory that is not available. "+
			"When servicing the page not fault, if disk IO is required, it is considered a minor fault.",
		[]string{"domain"},
		nil)
	libvirtDomainMemoryStatUnusedBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_memory_stats", "unused_bytes"),
		"The amount of memory left completely unused by the system. Memory that is available but used for "+
			"reclaimable caches should NOT be reported as free. This value is expressed in bytes.",
		[]string{"domain"},
		nil)
	libvirtDomainMemoryStatAvailableBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_memory_stats", "available_bytes"),
		"The total amount of usable memory as seen by the domain. This value may be less than the amount of "+
			"memory assigned to the domain if a balloon driver is in use or if the guest OS does not initialize all "+
			"assigned pages. This value is expressed in bytes.",
		[]string{"domain"},
		nil)
	libvirtDomainMemoryStatActualBaloonBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_memory_stats", "actual_balloon_bytes"),
		"Current balloon value (in bytes).",
		[]string{"domain"},
		nil)
	libvirtDomainMemoryStatRssBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_memory_stats", "rss_bytes"),
		"Resident Set Size of the process running the domain. This value is in bytes",
		[]string{"domain"},
		nil)
	libvirtDomainMemoryStatUsableBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_memory_stats", "usable_bytes"),
		"How much the balloon can be inflated without pushing the guest system to swap, corresponds "+
			"to 'Available' in /proc/meminfo",
		[]string{"domain"},
		nil)
	libvirtDomainMemoryStatDiskCachesBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_memory_stats", "disk_cache_bytes"),
		"The amount of memory, that can be quickly reclaimed without additional I/O (in bytes)."+
			"Typically these pages are used for caching files from disk.",
		[]string{"domain"},
		nil)
	libvirtDomainMemoryStatUsedPercentDesc = prometheus.NewDesc(
		prometheus.BuildFQName("libvirt", "domain_memory_stats", "used_percent"),
		"The amount of memory in percent, that used by domain.",
		[]string{"domain"},
		nil)

	errorsMap map[string]struct{}
)

// Write message to stdout only once for the concrete error
// "err" - an error message
// "name" - name of an error, to count it
func WriteErrorOnce(err string, name string) {
	if _, ok := errorsMap[name]; !ok {
		log.Printf("%s", err)
		errorsMap[name] = struct{}{}
	}
}

// CollectDomain extracts Prometheus metrics from a libvirt domain.
func CollectDomain(ch chan<- prometheus.Metric, stat libvirt.DomainStats) error {
	domainName, err := stat.Domain.GetName()
	if err != nil {
		return err
	}

	domainUUID, err := stat.Domain.GetUUIDString()
	if err != nil {
		return err
	}

	// Decode XML description of domain to get block device names, etc.
	xmlDesc, err := stat.Domain.GetXMLDesc(0)
	if err != nil {
		return err
	}

	var desc libvirtSchema.Domain
	err = xml.Unmarshal([]byte(xmlDesc), &desc)
	if err != nil {
		return err
	}

	// Report domain info.
	info, err := stat.Domain.GetInfo()
	if err != nil {
		return err
	}

	domain_mold_name := domainName
	display_mold_name := domainName
	vm_user_name := "N/A"
	moldMetaMutex.RLock()
	for _, val := range domainMetaInfo {
		if domainName == val["domain_name"] {
			domain_mold_name = val["domain_mold_name"]
			display_mold_name = display_mold_name + " ( " + domain_mold_name + " )"
			vm_user_name = val["vm_user_name"]
		}
	}
	moldMetaMutex.RUnlock()

	ch <- prometheus.MustNewConstMetric(
		libvirtDomainInfoMetaDesc,
		prometheus.GaugeValue,
		float64(1),
		domainName,
		domainUUID,
		desc.Metadata.NovaInstance.NovaName,
		desc.Metadata.NovaInstance.NovaFlavor.FlavorName,
		desc.Metadata.NovaInstance.NovaOwner.NovaUser.UserName,
		desc.Metadata.NovaInstance.NovaOwner.NovaUser.UserUUID,
		desc.Metadata.NovaInstance.NovaOwner.NovaProject.ProjectName,
		desc.Metadata.NovaInstance.NovaOwner.NovaProject.ProjectUUID,
		desc.Metadata.NovaInstance.NovaRoot.RootType,
		desc.Metadata.NovaInstance.NovaRoot.RootUUID,
		domain_mold_name,
		vm_user_name,
		display_mold_name)
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainInfoMaxMemBytesDesc,
		prometheus.GaugeValue,
		float64(info.MaxMem)*1024,
		domainName)
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainInfoMemoryUsageBytesDesc,
		prometheus.GaugeValue,
		float64(info.Memory)*1024,
		domainName)
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainInfoNrVirtCPUDesc,
		prometheus.GaugeValue,
		float64(info.NrVirtCpu),
		domainName)
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainInfoCPUTimeDesc,
		prometheus.CounterValue,
		float64(info.CpuTime)/1000/1000/1000, // From nsec to sec
		domainName)
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainInfoVirDomainState,
		prometheus.GaugeValue,
		float64(info.State),
		domainName)

	domainStatsVcpu, err := stat.Domain.GetVcpus()
	if err != nil {
		lverr, ok := err.(libvirt.Error)
		if !ok || lverr.Code != libvirt.ERR_OPERATION_INVALID {
			return err
		}
	} else {
		for _, vcpu := range domainStatsVcpu {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainVcpuStateDesc,
				prometheus.GaugeValue,
				float64(vcpu.State),
				domainName,
				strconv.FormatInt(int64(vcpu.Number), 10))

			ch <- prometheus.MustNewConstMetric(
				libvirtDomainVcpuTimeDesc,
				prometheus.CounterValue,
				float64(vcpu.CpuTime)/1000/1000/1000, // From nsec to sec
				domainName,
				strconv.FormatInt(int64(vcpu.Number), 10))

			ch <- prometheus.MustNewConstMetric(
				libvirtDomainVcpuCPUDesc,
				prometheus.GaugeValue,
				float64(vcpu.Cpu),
				domainName,
				strconv.FormatInt(int64(vcpu.Number), 10))
		}
	}

	if enableStatsVcpu {
		/* There's no Wait in GetVcpus()
		 * But there's no cpu number in libvirt.DomainStats
		 * Time and State are present in both structs
		 * So, let's take Wait here
		 */
		for cpuNum, vcpu := range stat.Vcpu {
			if vcpu.WaitSet {
				ch <- prometheus.MustNewConstMetric(
					libvirtDomainVcpuWaitDesc,
					prometheus.CounterValue,
					float64(vcpu.Wait)/1000/1000/1000,
					domainName,
					strconv.FormatInt(int64(cpuNum), 10))
			}
			if vcpu.DelaySet {
				ch <- prometheus.MustNewConstMetric(
					libvirtDomainVcpuDelayDesc,
					prometheus.CounterValue,
					float64(vcpu.Delay)/1e9,
					domainName,
					strconv.FormatInt(int64(cpuNum), 10))
			}
		}
	}

	// Report block device statistics.
	for _, disk := range stat.Block {
		var DiskSource string
		var Device *libvirtSchema.Disk
		// Ugly hack to avoid getting metrics from cdrom block device
		// TODO: somehow check the disk 'device' field for 'cdrom' string
		if disk.Name == "hdc" || disk.Name == "hda" {
			continue
		}
		/*  "block.<num>.path" - string describing the source of block device <num>,
		    if it is a file or block device (omitted for network
		    sources and drives with no media inserted). For network device (i.e. rbd) take from xml. */
		for _, dev := range desc.Devices.Disks {
			if dev.Target.Device == disk.Name {
				if disk.PathSet {
					DiskSource = disk.Path
				} else {
					DiskSource = dev.Source.Name
				}
				Device = &dev
				break
			}
		}

		mold_disk_name := "N/A"
		display_volume_name := disk.Name
		mold_volume_type := "N/A"
		moldMetaMutex.RLock()
		for _, val := range diskMetaInfo {
			if domainName == val["domain_name"] && strings.Contains(strings.ReplaceAll(DiskSource, "-", ""), strings.ReplaceAll(val["disk_path"], "-", "")) {
				mold_disk_name = val["mold_disk_name"]
				display_volume_name = display_volume_name + " ( " + mold_disk_name + " )"
				mold_volume_type = val["volume_type"]
			}
		}
		moldMetaMutex.RUnlock()
		ch <- prometheus.MustNewConstMetric(
			libvirtDomainMetaBlockDesc,
			prometheus.GaugeValue,
			float64(1),
			domainName,
			disk.Name,
			DiskSource,
			Device.Serial,
			Device.Target.Bus,
			Device.DiskType,
			Device.Driver.Type,
			Device.Driver.Cache,
			Device.Driver.Discard,
			mold_disk_name,
			display_volume_name,
			mold_volume_type)

		// https://libvirt.org/html/libvirt-libvirt-domain.html#virConnectGetAllDomainStats
		if disk.RdBytesSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainBlockRdBytesDesc,
				prometheus.CounterValue,
				float64(disk.RdBytes),
				domainName,
				disk.Name)
		}
		if disk.RdReqsSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainBlockRdReqDesc,
				prometheus.CounterValue,
				float64(disk.RdReqs),
				domainName,
				disk.Name)
		}
		if disk.RdTimesSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainBlockRdTotalTimeSecondsDesc,
				prometheus.CounterValue,
				float64(disk.RdTimes)/1e9,
				domainName,
				disk.Name)
		}
		if disk.WrBytesSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainBlockWrBytesDesc,
				prometheus.CounterValue,
				float64(disk.WrBytes),
				domainName,
				disk.Name)
		}
		if disk.WrReqsSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainBlockWrReqDesc,
				prometheus.CounterValue,
				float64(disk.WrReqs),
				domainName,
				disk.Name)
		}
		if disk.WrTimesSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainBlockWrTotalTimesDesc,
				prometheus.CounterValue,
				float64(disk.WrTimes)/1e9,
				domainName,
				disk.Name)
		}
		if disk.FlReqsSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainBlockFlushReqDesc,
				prometheus.CounterValue,
				float64(disk.FlReqs),
				domainName,
				disk.Name)
		}
		if disk.FlTimesSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainBlockFlushTotalTimeSecondsDesc,
				prometheus.CounterValue,
				float64(disk.FlTimes)/1e9,
				domainName,
				disk.Name)
		}
		if disk.AllocationSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainBlockAllocationDesc,
				prometheus.GaugeValue,
				float64(disk.Allocation),
				domainName,
				disk.Name)
		}
		if disk.CapacitySet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainBlockCapacityBytesDesc,
				prometheus.GaugeValue,
				float64(disk.Capacity),
				domainName,
				disk.Name)
		}
		if disk.PhysicalSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainBlockPhysicalSizeBytesDesc,
				prometheus.GaugeValue,
				float64(disk.Physical),
				domainName,
				disk.Name)
		}

		if enableDomainBlockIoTune {
			blockIOTuneParams, err := stat.Domain.GetBlockIoTune(disk.Name, 0)
			if err != nil {
				lverr, ok := err.(libvirt.Error)
				if !ok {
					switch lverr.Code {
					case libvirt.ERR_OPERATION_INVALID:
						// This should be one-shot error
						log.Printf("Invalid operation GetBlockIoTune: %s", err.Error())
					case libvirt.ERR_OPERATION_UNSUPPORTED:
						WriteErrorOnce("Unsupported operation GetBlockIoTune: "+err.Error(), "blkiotune_unsupported")
					default:
						return err
					}
				}
			} else {
				if blockIOTuneParams.TotalBytesSecSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockTotalBytesSecDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.TotalBytesSec),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.ReadBytesSecSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockReadBytesSecDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.ReadBytesSec),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.WriteBytesSecSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockWriteBytesSecDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.WriteBytesSec),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.TotalIopsSecSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockTotalIopsSecDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.TotalIopsSec),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.ReadIopsSecSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockReadIopsSecDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.ReadIopsSec),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.WriteIopsSecSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockWriteIopsSecDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.WriteIopsSec),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.TotalBytesSecMaxSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockTotalBytesSecMaxDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.TotalBytesSecMax),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.ReadBytesSecMaxSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockReadBytesSecMaxDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.ReadBytesSecMax),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.WriteBytesSecMaxSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockWriteBytesSecMaxDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.WriteBytesSecMax),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.TotalIopsSecMaxSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockTotalIopsSecMaxDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.TotalIopsSecMax),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.ReadIopsSecMaxSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockReadIopsSecMaxDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.ReadIopsSecMax),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.WriteIopsSecMaxSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockWriteIopsSecMaxDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.WriteIopsSecMax),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.TotalBytesSecMaxLengthSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockTotalBytesSecMaxLengthDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.TotalBytesSecMaxLength),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.ReadBytesSecMaxLengthSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockReadBytesSecMaxLengthDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.ReadBytesSecMaxLength),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.WriteBytesSecMaxLengthSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockWriteBytesSecMaxLengthDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.WriteBytesSecMaxLength),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.TotalIopsSecMaxLengthSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockTotalIopsSecMaxLengthDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.TotalIopsSecMaxLength),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.ReadIopsSecMaxLengthSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockReadIopsSecMaxLengthDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.ReadIopsSecMaxLength),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.WriteIopsSecMaxLengthSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockWriteIopsSecMaxLengthDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.WriteIopsSecMaxLength),
						domainName,
						disk.Name)
				}
				if blockIOTuneParams.SizeIopsSecSet {
					ch <- prometheus.MustNewConstMetric(
						libvirtDomainBlockSizeIopsSecDesc,
						prometheus.GaugeValue,
						float64(blockIOTuneParams.SizeIopsSec),
						domainName,
						disk.Name)
				}
			}
		}
	}

	checkFsinfo(domainName, ch)

	// Report network interface statistics.
	for _, iface := range stat.Net {
		var SourceBridge string
		var MacAddress string
		var VirtualInterface string
		// Additional info for ovs network
		for _, net := range desc.Devices.Interfaces {
			if net.Target.Device == iface.Name {
				SourceBridge = net.Source.Bridge
				VirtualInterface = net.Virtualport.Parameters.InterfaceID
				MacAddress = net.Mac.Address
				break
			}
		}

		if SourceBridge != "" || VirtualInterface != "" || MacAddress != "" {

			mold_network_name := "N/A"
			moldMetaMutex.RLock()
			for _, val := range networkMetaInfo {
				if MacAddress == val["mac_addr"] && val["mold_network_name"] != "%!s(<nil>)" {
					mold_network_name = val["mold_network_name"]
				}
			}
			moldMetaMutex.RUnlock()

			ch <- prometheus.MustNewConstMetric(
				libvirtDomainMetaInterfacesDesc,
				prometheus.GaugeValue,
				float64(1),
				domainName,
				SourceBridge,
				iface.Name,
				VirtualInterface,
				MacAddress,
				mold_network_name)
		}
		if iface.RxBytesSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainInterfaceRxBytesDesc,
				prometheus.CounterValue,
				float64(iface.RxBytes),
				domainName,
				iface.Name)
		}
		if iface.RxPktsSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainInterfaceRxPacketsDesc,
				prometheus.CounterValue,
				float64(iface.RxPkts),
				domainName,
				iface.Name)
		}
		if iface.RxErrsSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainInterfaceRxErrsDesc,
				prometheus.CounterValue,
				float64(iface.RxErrs),
				domainName,
				iface.Name)
		}
		if iface.RxDropSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainInterfaceRxDropDesc,
				prometheus.CounterValue,
				float64(iface.RxDrop),
				domainName,
				iface.Name)
		}
		if iface.TxBytesSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainInterfaceTxBytesDesc,
				prometheus.CounterValue,
				float64(iface.TxBytes),
				domainName,
				iface.Name)
		}
		if iface.TxPktsSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainInterfaceTxPacketsDesc,
				prometheus.CounterValue,
				float64(iface.TxPkts),
				domainName,
				iface.Name)
		}
		if iface.TxErrsSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainInterfaceTxErrsDesc,
				prometheus.CounterValue,
				float64(iface.TxErrs),
				domainName,
				iface.Name)
		}
		if iface.TxDropSet {
			ch <- prometheus.MustNewConstMetric(
				libvirtDomainInterfaceTxDropDesc,
				prometheus.CounterValue,
				float64(iface.TxDrop),
				domainName,
				iface.Name)
		}
	}

	// Collect Memory Stats
	memorystat, memErr := stat.Domain.MemoryStats(11, 0)
	var MemoryStats libvirtSchema.VirDomainMemoryStats
	var usedPercent float64
	if memErr == nil {
		MemoryStats = memoryStatCollect(&memorystat)
		if MemoryStats.Usable != 0 && MemoryStats.Available != 0 {
			usedPercent = (float64(MemoryStats.Available) - float64(MemoryStats.Usable)) / (float64(MemoryStats.Available) / float64(100))
		}

	}
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainMemoryStatMajorFaultTotalDesc,
		prometheus.CounterValue,
		float64(MemoryStats.MajorFault),
		domainName)
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainMemoryStatMinorFaultTotalDesc,
		prometheus.CounterValue,
		float64(MemoryStats.MinorFault),
		domainName)
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainMemoryStatUnusedBytesDesc,
		prometheus.GaugeValue,
		float64(MemoryStats.Unused)*1024,
		domainName)
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainMemoryStatAvailableBytesDesc,
		prometheus.GaugeValue,
		float64(MemoryStats.Available)*1024,
		domainName)
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainMemoryStatActualBaloonBytesDesc,
		prometheus.GaugeValue,
		float64(MemoryStats.ActualBalloon)*1024,
		domainName)
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainMemoryStatRssBytesDesc,
		prometheus.GaugeValue,
		float64(MemoryStats.Rss)*1024,
		domainName)
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainMemoryStatUsableBytesDesc,
		prometheus.GaugeValue,
		float64(MemoryStats.Usable)*1024,
		domainName)
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainMemoryStatDiskCachesBytesDesc,
		prometheus.GaugeValue,
		float64(MemoryStats.DiskCaches)*1024,
		domainName)
	ch <- prometheus.MustNewConstMetric(
		libvirtDomainMemoryStatUsedPercentDesc,
		prometheus.GaugeValue,
		float64(usedPercent),
		domainName)

	return nil
}

// CollectFromLibvirt obtains Prometheus metrics from all domains in a
// libvirt setup.
func CollectFromLibvirt(ch chan<- prometheus.Metric, uri string) error {
	conn, err := libvirt.NewConnectReadOnly(uri)
	if err != nil {
		return err
	}
	defer conn.Close()

	hypervisorVersionNum, err := conn.GetVersion() // virConnectGetVersion, hypervisor running, e.g. QEMU
	if err != nil {
		return err
	}
	hypervisorVersion := fmt.Sprintf("%d.%d.%d", hypervisorVersionNum/1000000%1000, hypervisorVersionNum/1000%1000, hypervisorVersionNum%1000)

	libvirtdVersionNum, err := conn.GetLibVersion() // virConnectGetLibVersion, libvirt daemon running
	if err != nil {
		return err
	}
	libvirtdVersion := fmt.Sprintf("%d.%d.%d", libvirtdVersionNum/1000000%1000, libvirtdVersionNum/1000%1000, libvirtdVersionNum%1000)

	libraryVersionNum, err := libvirt.GetVersion() // virGetVersion, version of libvirt (dynamic) library used by this binary (exporter), not the daemon version
	if err != nil {
		return err
	}
	libraryVersion := fmt.Sprintf("%d.%d.%d", libraryVersionNum/1000000%1000, libraryVersionNum/1000%1000, libraryVersionNum%1000)

	ch <- prometheus.MustNewConstMetric(
		libvirtVersionsInfoDesc,
		prometheus.GaugeValue,
		1.0,
		hypervisorVersion,
		libvirtdVersion,
		libraryVersion)

	domains, err := conn.ListAllDomains(libvirt.CONNECT_LIST_DOMAINS_RUNNING)
	if err != nil {
		return err
	}
	defer func(domains []libvirt.Domain) {
		for i := range domains {
			domains[i].Free()
		}
	}(domains)

	statsTypes := libvirt.DOMAIN_STATS_STATE | libvirt.DOMAIN_STATS_CPU_TOTAL |
		libvirt.DOMAIN_STATS_INTERFACE | libvirt.DOMAIN_STATS_BALLOON |
		libvirt.DOMAIN_STATS_BLOCK | libvirt.DOMAIN_STATS_VCPU | libvirt.DOMAIN_STATS_MEMORY
	if enableStatsPerf {
		statsTypes |= libvirt.DOMAIN_STATS_PERF
	}

	for i := range domains {
		domainName, err := domains[i].GetName()
		if err == nil {
			excluded := false
			for pattern := range excludedDomainNames {
				if strings.HasPrefix(domainName, pattern) {
					excluded = true
					break
				}
			}
			if excluded {
				continue
			}
		}

		if domainName == "" {
			log.Printf("Failed to get domain name: %s", err)
			continue
		}

		stats, err := getDomainStatsWithTimeout(uri, domainName, statsTypes)
		if err != nil {
			log.Printf("Failed to get domain stats: %s", err)
			continue
		}

		func(stats []libvirt.DomainStats) {
			defer func(stats []libvirt.DomainStats) {
				for _, stat := range stats {
					stat.Domain.Free()
				}
			}(stats)

			for _, stat := range stats {
				if err := CollectDomain(ch, stat); err != nil {
					log.Printf("Failed to scrape metrics: %s", err)
				}
			}
		}(stats)
	}
	return nil
}

func getDomainStatsWithTimeout(
	uri string,
	domainName string,
	statsTypes libvirt.DomainStatsTypes,
) ([]libvirt.DomainStats, error) {
	collectable, err := domainJobTypeIsNone(uri, domainName)
	if err != nil {
		return nil, err
	}
	if !collectable {
		return []libvirt.DomainStats{}, nil
	}

	resultCh := make(chan domainStatsResult, 1)

	go func() {
		domainConn, err := libvirt.NewConnectReadOnly(uri)
		if err != nil {
			resultCh <- domainStatsResult{err: err}
			return
		}
		defer domainConn.Close()

		domain, err := domainConn.LookupDomainByName(domainName)
		if err != nil {
			resultCh <- domainStatsResult{err: err}
			return
		}
		defer domain.Free()

		stats, err := domainConn.GetAllDomainStats(
			[]*libvirt.Domain{domain},
			statsTypes,
			libvirt.CONNECT_GET_ALL_DOMAINS_STATS_NOWAIT,
		)
		resultCh <- domainStatsResult{
			stats: stats,
			err:   err,
		}
	}()

	timer := time.NewTimer(domStatsTimeoutDuration)
	defer timer.Stop()

	select {
	case result := <-resultCh:
		return result.stats, result.err
	case <-timer.C:
		return nil, fmt.Errorf("timed out after %s for domain %s", domStatsTimeoutDuration, domainName)
	}
}

func domainJobTypeIsNone(uri string, domainName string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), domStatsTimeoutDuration)
	defer cancel()

	output, err := exec.CommandContext(ctx, "virsh", "--connect", uri, "domjobinfo", domainName).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return false, fmt.Errorf("timed out after %s checking domjobinfo for domain %s", domStatsTimeoutDuration, domainName)
	}
	if err != nil {
		errText := strings.TrimSpace(string(output))
		if errText != "" {
			return false, fmt.Errorf("virsh domjobinfo failed for %s: %s", domainName, errText)
		}
		return false, err
	}

	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Job type:") {
			continue
		}
		jobType := strings.TrimSpace(strings.TrimPrefix(line, "Job type:"))
		return jobType == "None", nil
	}

	return false, fmt.Errorf("virsh domjobinfo output missing job type for domain %s", domainName)
}

func memoryStatCollect(memorystat *[]libvirt.DomainMemoryStat) libvirtSchema.VirDomainMemoryStats {
	var MemoryStats libvirtSchema.VirDomainMemoryStats
	for _, domainmemorystat := range *memorystat {
		switch tag := domainmemorystat.Tag; tag {
		case 2:
			MemoryStats.MajorFault = domainmemorystat.Val
		case 3:
			MemoryStats.MinorFault = domainmemorystat.Val
		case 4:
			MemoryStats.Unused = domainmemorystat.Val
		case 5:
			MemoryStats.Available = domainmemorystat.Val
		case 6:
			MemoryStats.ActualBalloon = domainmemorystat.Val
		case 7:
			MemoryStats.Rss = domainmemorystat.Val
		case 8:
			MemoryStats.Usable = domainmemorystat.Val
		case 10:
			MemoryStats.DiskCaches = domainmemorystat.Val
		}
	}
	return MemoryStats
}

// LibvirtExporter implements a Prometheus exporter for libvirt state.
type LibvirtExporter struct {
	uri string
}

// NewLibvirtExporter creates a new Prometheus exporter for libvirt.
func NewLibvirtExporter(uri string) (*LibvirtExporter, error) {
	return &LibvirtExporter{
		uri: uri,
	}, nil
}

// Describe returns metadata for all Prometheus metrics that may be exported.
func (e *LibvirtExporter) Describe(ch chan<- *prometheus.Desc) {
	// Status and versions
	ch <- libvirtUpDesc
	ch <- libvirtMoldCollectDesc
	ch <- libvirtVersionsInfoDesc

	// Domain info
	ch <- libvirtDomainInfoMetaDesc
	ch <- libvirtDomainInfoMaxMemBytesDesc
	ch <- libvirtDomainInfoMemoryUsageBytesDesc
	ch <- libvirtDomainInfoNrVirtCPUDesc
	ch <- libvirtDomainInfoCPUTimeDesc
	ch <- libvirtDomainInfoVirDomainState

	// VCPU info
	ch <- libvirtDomainVcpuStateDesc
	ch <- libvirtDomainVcpuTimeDesc
	ch <- libvirtDomainVcpuDelayDesc
	ch <- libvirtDomainVcpuCPUDesc
	ch <- libvirtDomainVcpuWaitDesc

	// Domain block stats
	ch <- libvirtDomainMetaBlockDesc
	ch <- libvirtDomainBlockRdBytesDesc
	ch <- libvirtDomainBlockRdReqDesc
	ch <- libvirtDomainBlockRdTotalTimeSecondsDesc
	ch <- libvirtDomainBlockWrBytesDesc
	ch <- libvirtDomainBlockWrReqDesc
	ch <- libvirtDomainBlockWrTotalTimesDesc
	ch <- libvirtDomainBlockFlushReqDesc
	ch <- libvirtDomainBlockFlushTotalTimeSecondsDesc
	ch <- libvirtDomainBlockAllocationDesc
	ch <- libvirtDomainBlockCapacityBytesDesc
	ch <- libvirtDomainBlockPhysicalSizeBytesDesc

	// Domain fsinfo stats
	ch <- libvirtDomainFsInfoAgentStatusDesc
	ch <- libvirtDomainFsInfoTotalBytesDesc
	ch <- libvirtDomainFsInfoUsageBytesDesc

	// Domain net interfaces stats
	ch <- libvirtDomainMetaInterfacesDesc
	ch <- libvirtDomainInterfaceRxBytesDesc
	ch <- libvirtDomainInterfaceRxPacketsDesc
	ch <- libvirtDomainInterfaceRxErrsDesc
	ch <- libvirtDomainInterfaceRxDropDesc
	ch <- libvirtDomainInterfaceTxBytesDesc
	ch <- libvirtDomainInterfaceTxPacketsDesc
	ch <- libvirtDomainInterfaceTxErrsDesc
	ch <- libvirtDomainInterfaceTxDropDesc

	// Domain memory stats
	ch <- libvirtDomainMemoryStatMajorFaultTotalDesc
	ch <- libvirtDomainMemoryStatMinorFaultTotalDesc
	ch <- libvirtDomainMemoryStatUnusedBytesDesc
	ch <- libvirtDomainMemoryStatAvailableBytesDesc
	ch <- libvirtDomainMemoryStatActualBaloonBytesDesc
	ch <- libvirtDomainMemoryStatRssBytesDesc
	ch <- libvirtDomainMemoryStatUsableBytesDesc
	ch <- libvirtDomainMemoryStatDiskCachesBytesDesc
}

// Collect scrapes Prometheus metrics from libvirt.
func (e *LibvirtExporter) Collect(ch chan<- prometheus.Metric) {
	CollectMoldMeta(ch)

	err := CollectFromLibvirt(ch, e.uri)
	if err == nil {
		ch <- prometheus.MustNewConstMetric(
			libvirtUpDesc,
			prometheus.GaugeValue,
			1.0)
	} else {
		log.Printf("Failed to scrape metrics: %s", err)
		ch <- prometheus.MustNewConstMetric(
			libvirtUpDesc,
			prometheus.GaugeValue,
			0.0)
	}
}

func NewAesCipher(key []byte) (*AESCipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return &AESCipher{block}, nil
}

func (a *AESCipher) DecryptString(base64String string) string {

	b, _ := base64.URLEncoding.DecodeString(base64String)
	byteString := []byte(b)

	decryptByteArray := make([]byte, len(byteString))
	iv := byteString[:aes.BlockSize]

	stream := cipher.NewCFBDecrypter(a.block, iv)
	stream.XORKeyStream(decryptByteArray, byteString[aes.BlockSize:])

	decPw := ""
	//byte 배열에 담긴 ascii 코드를 확인하여 32 (공백) 이상이면서 127(delete) 문자일 경우에 정상 문자로 인식
	for i, _ := range decryptByteArray {
		if decryptByteArray[i] > 31 && decryptByteArray[i] < 127 {
			decPw += string(decryptByteArray[i])
		}
	}

	return decPw
}

func loadTimeoutFromConfig(conf map[string]interface{}, key string, defaultTimeout time.Duration) time.Duration {
	raw, ok := conf[key]
	if !ok {
		return defaultTimeout
	}

	switch v := raw.(type) {
	case float64:
		if v > 0 {
			return time.Duration(v * float64(time.Second))
		}
	case string:
		sec, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil && sec > 0 {
			return time.Duration(sec) * time.Second
		}
	}

	log.Printf("invalid %s value in conf.json, using default %s", key, defaultTimeout)
	return defaultTimeout
}

func loadDurationSecondsFromConfig(conf map[string]interface{}, key string, defaultValue time.Duration) time.Duration {
	raw, ok := conf[key]
	if !ok {
		return defaultValue
	}

	parseSeconds := func(sec int) time.Duration {
		if sec < 0 {
			return time.Duration(-1) * time.Second
		}
		return time.Duration(sec) * time.Second
	}

	switch v := raw.(type) {
	case float64:
		return parseSeconds(int(v))
	case string:
		sec, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return parseSeconds(sec)
		}
	}

	log.Printf("invalid %s value in conf.json, using default %s", key, defaultValue)
	return defaultValue
}

func loadBoolFromConfig(conf map[string]interface{}, key string, defaultValue bool) bool {
	raw, ok := conf[key]
	if !ok {
		return defaultValue
	}

	switch v := raw.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		if err == nil {
			return parsed
		}
	}

	log.Printf("invalid %s value in conf.json, using default %t", key, defaultValue)
	return defaultValue
}

func loadStringSetFromConfig(conf map[string]interface{}, key string) map[string]struct{} {
	result := make(map[string]struct{})

	raw, ok := conf[key]
	if !ok {
		return result
	}

	switch v := raw.(type) {
	case []interface{}:
		for _, item := range v {
			name, ok := item.(string)
			if !ok {
				continue
			}
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			result[name] = struct{}{}
		}
	case string:
		for _, item := range strings.Split(v, ",") {
			name := strings.TrimSpace(item)
			if name == "" {
				continue
			}
			result[name] = struct{}{}
		}
	default:
		log.Printf("invalid %s value in conf.json, expected array or comma-separated string", key)
	}

	return result
}

func CollectMoldMeta(ch chan<- prometheus.Metric) {
	if moldMetaIntervalDuration > 0 {
		moldMetaMutex.RLock()
		useCache := !moldMetaLastRefresh.IsZero() && time.Since(moldMetaLastRefresh) < moldMetaIntervalDuration
		cachedStatus := moldMetaLastStatus
		moldMetaMutex.RUnlock()

		if useCache {
			ch <- prometheus.MustNewConstMetric(
				libvirtMoldCollectDesc,
				prometheus.GaugeValue,
				cachedStatus)
			return
		}
	}

	moldDbConf := confValue["mold_db"].(map[string]interface{})
	serverhost := moldDbConf["serverhost"].(string)
	port := moldDbConf["port"].(string)
	database := moldDbConf["database"].(string)
	username := moldDbConf["username"].(string)
	statusVal := 1.0
	refreshAt := time.Now()
	enpw := moldDbConf["password"].(string) // pw_encryption.go 파일로 암호화시킨 비밀번호
	domainMetaInfoTemp := map[int]map[string]string{}
	networkMetaInfoTemp := map[int]map[string]string{}
	diskMetaInfoTemp := map[int]map[string]string{}

	//키는 16, 24, 32만 가능합니다
	var key = []byte("ablestackwallkey") // pw_encryption.go 에서 암호화 한 방식과 동일한 key

	if password == "" {
		a, _ := NewAesCipher(key)
		password = a.DecryptString(enpw)
	}

	// sql.DB 객체 생성
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?timeout=%s",
		username, password, serverhost, port, database, moldDbConnectTimeoutDuration)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Println(err)
		statusVal = 0.0
	} else {
		defer db.Close()

		// domainMetaInfoQuery query
		domainMetaInfoQuery := " select vi.id as instance_id"
		domainMetaInfoQuery += "  , vi.name as domain_mold_name"
		domainMetaInfoQuery += "  , vi.instance_name as domain_name"
		domainMetaInfoQuery += "  , a.account_name as vm_user_name"
		domainMetaInfoQuery += " from vm_instance vi left join account a on vi.account_id = a.id"
		domainMetaInfoQuery += " where vi.removed is null"
		domainMetaInfoQuery += "  and a.removed is null"
		domainMetaInfoQuery += "  and vi.type = 'User'"
		doaminRows, doaminErr := db.Query(domainMetaInfoQuery)
		if doaminErr != nil {
			log.Println("domainMetaInfoQuery 정보를 수집할 수 없습니다.")
			log.Println(doaminErr)
			statusVal = 0.0
		} else {
			columns, _ := doaminRows.Columns()
			count := len(columns)
			values := make([]interface{}, count)
			valuePtrs := make([]interface{}, count)
			result_id := 0

			for doaminRows.Next() {
				for i, _ := range columns {
					valuePtrs[i] = &values[i]
				}
				doaminRows.Scan(valuePtrs...)

				tmp_struct := map[string]string{}

				for i, col := range columns {
					var v interface{}
					val := values[i]
					b, ok := val.([]byte)
					if ok {
						v = string(b)
					} else {
						v = val
					}
					tmp_struct[col] = fmt.Sprintf("%s", v)
				}

				domainMetaInfoTemp[result_id] = tmp_struct
				result_id++
			}

			//row 닫기. 안닫으면 too many connections  에러 오류 발생
			doaminRows.Close()
		}

		// networkMetaInfoQuery query
		networkMetaInfoQuery := " select ni.id as nic_id"
		networkMetaInfoQuery += "  , ni.mac_address as mac_addr"
		networkMetaInfoQuery += "  , ni.ip4_address as ip4_addr"
		networkMetaInfoQuery += "  , replace(ni.broadcast_uri, 'vlan://', '') as vlan_id"
		networkMetaInfoQuery += "  , nw.name as mold_network_name"
		networkMetaInfoQuery += "  , vi.name as domain_mold_name"
		networkMetaInfoQuery += "  , vi.instance_name as domain_name"
		networkMetaInfoQuery += " from nics ni left join networks nw on ni.network_id = nw.id left join vm_instance vi on ni.instance_id = vi.id"
		networkMetaInfoQuery += " where ni.removed is null"
		networkMetaInfoQuery += "  and nw.removed is null"
		networkMetaInfoQuery += "  and vi.removed is null"
		networkRows, networkErr := db.Query(networkMetaInfoQuery)
		if networkErr != nil {
			log.Println("networkMetaInfoQuery 정보를 수집할 수 없습니다.")
			log.Println(networkErr)
			statusVal = 0.0
		} else {
			columns, _ := networkRows.Columns()
			count := len(columns)
			values := make([]interface{}, count)
			valuePtrs := make([]interface{}, count)
			result_id := 0

			for networkRows.Next() {
				for i, _ := range columns {
					valuePtrs[i] = &values[i]
				}
				networkRows.Scan(valuePtrs...)

				tmp_struct := map[string]string{}

				for i, col := range columns {
					var v interface{}
					val := values[i]
					b, ok := val.([]byte)
					if ok {
						v = string(b)
					} else {
						v = val
					}
					tmp_struct[col] = fmt.Sprintf("%s", v)
				}

				networkMetaInfoTemp[result_id] = tmp_struct
				result_id++
			}

			//row 닫기. 안닫으면 too many connections  에러 오류 발생
			networkRows.Close()
		}

		// diskMetaInfoQuery query
		diskMetaInfoQuery := " select v.id as disk_id"
		diskMetaInfoQuery += "  , v.name as mold_disk_name"
		diskMetaInfoQuery += "  , v.path as disk_path"
		diskMetaInfoQuery += "  , v.volume_type as volume_type"
		diskMetaInfoQuery += "  , vi.name as domain_mold_name"
		diskMetaInfoQuery += "  , vi.instance_name as domain_name"
		diskMetaInfoQuery += "  , a.account_name as user_name"
		diskMetaInfoQuery += " from volumes v left join vm_instance vi on v.instance_id = vi.id left join account a on v.account_id = a.id"
		diskMetaInfoQuery += " where v.removed is null"
		diskMetaInfoQuery += "  and vi.removed is null"
		diskMetaInfoQuery += "  and a.removed is null"
		diskMetaInfoQuery += "  and vi.type = 'User'"
		diskRows, diskErr := db.Query(diskMetaInfoQuery)
		if diskErr != nil {
			log.Println("diskMetaInfoQuery 정보를 수집할 수 없습니다.")
			log.Println(diskErr)
			statusVal = 0.0
		} else {
			columns, _ := diskRows.Columns()
			count := len(columns)
			values := make([]interface{}, count)
			valuePtrs := make([]interface{}, count)
			result_id := 0

			for diskRows.Next() {
				for i, _ := range columns {
					valuePtrs[i] = &values[i]
				}
				diskRows.Scan(valuePtrs...)

				tmp_struct := map[string]string{}

				for i, col := range columns {
					var v interface{}
					val := values[i]
					b, ok := val.([]byte)
					if ok {
						v = string(b)
					} else {
						v = val
					}
					tmp_struct[col] = fmt.Sprintf("%s", v)
				}

				diskMetaInfoTemp[result_id] = tmp_struct
				result_id++
			}

			//row 닫기. 안닫으면 too many connections  에러 오류 발생
			diskRows.Close()
		}
	}

	moldMetaMutex.Lock()
	if statusVal == 1.0 {
		domainMetaInfo = domainMetaInfoTemp
		networkMetaInfo = networkMetaInfoTemp
		diskMetaInfo = diskMetaInfoTemp
	}
	moldMetaLastStatus = statusVal
	moldMetaLastRefresh = refreshAt
	moldMetaMutex.Unlock()

	ch <- prometheus.MustNewConstMetric(
		libvirtMoldCollectDesc,
		prometheus.GaugeValue,
		statusVal)
}

func emitFsInfoMetrics(domainName string, entry fsInfoCacheEntry, ch chan<- prometheus.Metric) {
	for _, metric := range entry.metrics {
		ch <- prometheus.MustNewConstMetric(
			libvirtDomainFsInfoTotalBytesDesc,
			prometheus.GaugeValue,
			metric.totalBytes,
			domainName,
			metric.partitionName,
			metric.partitionMountpoint,
			metric.partitionType,
			metric.serial)

		ch <- prometheus.MustNewConstMetric(
			libvirtDomainFsInfoUsageBytesDesc,
			prometheus.GaugeValue,
			metric.usedBytes,
			domainName,
			metric.partitionName,
			metric.partitionMountpoint,
			metric.partitionType,
			metric.serial)
	}

	ch <- prometheus.MustNewConstMetric(
		libvirtDomainFsInfoAgentStatusDesc,
		prometheus.GaugeValue,
		entry.status,
		domainName)
}

func collectFsInfo(domainName string) fsInfoCacheEntry {
	entry := fsInfoCacheEntry{
		updatedAt: time.Now(),
		status:    1, // command or parse error by default
		metrics:   []fsInfoMetric{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), qemuAgentTimeoutDuration)
	defer cancel()

	cmd := exec.CommandContext(ctx, "virsh", "qemu-agent-command", domainName, "{\"execute\": \"guest-get-fsinfo\"}", "--pretty")

	jsonString, err := cmd.CombinedOutput()
	if err != nil {
		return entry
	}

	var data ReturnData
	if err := json.Unmarshal(jsonString, &data); err != nil {
		return entry
	}

	notSupportedAgent := true
	for _, partition := range data.Return {
		if len(partition.Disk) == 0 || partition.TotalBytes <= 0 {
			continue
		}

		notSupportedAgent = false
		serial := ""
		for _, disk := range partition.Disk {
			if len(disk.Serial) >= 20 {
				serial = disk.Serial[len(disk.Serial)-20:]
			} else {
				serial = disk.Serial
			}
		}

		entry.metrics = append(entry.metrics, fsInfoMetric{
			partitionName:       partition.Name,
			partitionMountpoint: partition.Mountpoint,
			partitionType:       partition.Type,
			serial:              serial,
			totalBytes:          float64(partition.TotalBytes),
			usedBytes:           float64(partition.UsedBytes),
		})
	}

	if notSupportedAgent {
		entry.status = 2
	} else {
		entry.status = 0
	}
	return entry
}

func checkFsinfo(domainName string, ch chan<- prometheus.Metric) {
	if fsInfoIntervalDuration < 0 {
		return
	}

	if fsInfoIntervalDuration > 0 {
		fsInfoCacheMutex.RLock()
		cached, ok := fsInfoCache[domainName]
		fsInfoCacheMutex.RUnlock()
		if ok && time.Since(cached.updatedAt) < fsInfoIntervalDuration {
			emitFsInfoMetrics(domainName, cached, ch)
			return
		}
	}

	entry := collectFsInfo(domainName)
	if fsInfoIntervalDuration > 0 {
		fsInfoCacheMutex.Lock()
		fsInfoCache[domainName] = entry
		fsInfoCacheMutex.Unlock()
	}
	emitFsInfoMetrics(domainName, entry, ch)
}

func main() {
	var (
		app           = kingpin.New("libvirt_exporter", "Prometheus metrics exporter for libvirt")
		confPath      = app.Flag("conf.path", "confing path.").Default("./conf.json").String()
		listenAddress = app.Flag("web.listen-address", "Address to listen on for web interface and telemetry.").Default(":3002").String()
		metricsPath   = app.Flag("web.telemetry-path", "Path under which to expose metrics.").Default("/metrics").String()
		libvirtURI    = app.Flag("libvirt.uri", "Libvirt URI from which to extract metrics.").Default("qemu:///system").String()
	)

	kingpin.MustParse(app.Parse(os.Args[1:]))

	errorsMap = make(map[string]struct{})

	// Open config json file
	jsonFile, err := os.Open(*confPath)
	if err != nil {
		log.Fatal(err)
	}

	// defer the closing of our jsonFile so that we can parse it later on
	defer jsonFile.Close()

	val, err := ioutil.ReadAll(jsonFile)
	if err != nil {
		log.Fatal(err)
	}
	byteValue = val
	if err := json.Unmarshal(byteValue, &confValue); err != nil {
		log.Fatal(err)
	}
	webTimeoutDuration = loadTimeoutFromConfig(confValue, "web_timeout_seconds", 10*time.Second)
	qemuAgentTimeoutDuration = loadTimeoutFromConfig(confValue, "qemu_agent_timeout_seconds", 1*time.Second)
	domStatsTimeoutDuration = loadTimeoutFromConfig(confValue, "domstats_timeout_seconds", 2*time.Second)
	fsInfoIntervalDuration = loadDurationSecondsFromConfig(confValue, "fsinfo_interval_seconds", 0*time.Second)
	enableStatsPerf = loadBoolFromConfig(confValue, "enable_stats_perf", false)
	enableStatsVcpu = loadBoolFromConfig(confValue, "enable_stats_vcpu", false)
	enableDomainBlockIoTune = loadBoolFromConfig(confValue, "enable_domain_block_io_tune", false)
	moldDbConnectTimeoutDuration = loadTimeoutFromConfig(confValue, "mold_db_connect_timeout_seconds", 3*time.Second)
	moldMetaIntervalDuration = loadDurationSecondsFromConfig(confValue, "mold_meta_interval_seconds", 120*time.Second)
	excludedDomainNames = loadStringSetFromConfig(confValue, "exclude_domain_names")

	exporter, err := NewLibvirtExporter(*libvirtURI)
	if err != nil {
		panic(err)
	}
	prometheus.MustRegister(exporter)

	http.Handle(*metricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`
			<html>
			<head><title>Libvirt Exporter</title></head>
			<body>
			<h1>Libvirt Exporter</h1>
			<p><a href='` + *metricsPath + `'>Metrics</a></p>
			</body>
			</html>`))
	})
	server := &http.Server{
		Addr:              *listenAddress,
		Handler:           nil,
		ReadHeaderTimeout: webTimeoutDuration,
		ReadTimeout:       webTimeoutDuration,
		WriteTimeout:      webTimeoutDuration,
		IdleTimeout:       webTimeoutDuration,
	}

	log.Fatal(server.ListenAndServe())
}
