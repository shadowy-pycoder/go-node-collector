// Modifications copyright 2024 shadowy-pycoder
// Copyright 2015 The Prometheus Authors
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

package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/procfs"
	"github.com/prometheus/procfs/sysfs"
	"golang.org/x/exp/maps"
)

const (
	// Idle jump back limit in seconds.
	jumpBackSeconds       = 3.0
	cpuCollectorSubsystem = "cpu"
)

var nodeCPUSecondsDesc = prometheus.NewDesc(
	prometheus.BuildFQName(namespace, cpuCollectorSubsystem, "seconds_total"),
	"Seconds the CPUs spent in each mode.",
	[]string{"cpu", "mode"}, nil,
)

func init() {
	registerCollector("cpu", newCPUCollector)
}

func readUintFromFile(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, err
	}
	return value, nil
}

type cpuCollector struct {
	fs                 procfs.FS
	cpu                *prometheus.Desc
	cpuInfo            *prometheus.Desc
	cpuFrequencyHz     *prometheus.Desc
	cpuFlagsInfo       *prometheus.Desc
	cpuBugsInfo        *prometheus.Desc
	cpuGuest           *prometheus.Desc
	cpuCoreThrottle    *prometheus.Desc
	cpuPackageThrottle *prometheus.Desc
	cpuIsolated        *prometheus.Desc
	cpuStats           map[int64]procfs.CPUStat
	cpuStatsMutex      sync.Mutex
	isolatedCpus       []uint16
}

// newCPUCollector returns a new Collector exposing kernel/system statistics.
func newCPUCollector() (Collector, error) {
	fs, err := procfs.NewDefaultFS()
	if err != nil {
		return nil, fmt.Errorf("failed to open procfs: %w", err)
	}

	sysfs, err := sysfs.NewDefaultFS()
	if err != nil {
		return nil, fmt.Errorf("failed to open sysfs: %w", err)
	}

	isolcpus, err := sysfs.IsolatedCPUs()
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("unable to get isolated cpus: %w", err)
		}
	}

	c := &cpuCollector{
		fs:  fs,
		cpu: nodeCPUSecondsDesc,
		cpuInfo: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, cpuCollectorSubsystem, "info"),
			"CPU information from /proc/cpuinfo.",
			[]string{"package", "core", "cpu", "vendor", "family", "model", "model_name", "microcode", "stepping", "cachesize"}, nil,
		),
		cpuFrequencyHz: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, cpuCollectorSubsystem, "frequency_hertz"),
			"CPU frequency in hertz from /proc/cpuinfo.",
			[]string{"package", "core", "cpu"}, nil,
		),
		cpuFlagsInfo: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, cpuCollectorSubsystem, "flag_info"),
			"The `flags` field of CPU information from /proc/cpuinfo taken from the first core.",
			[]string{"flag"}, nil,
		),
		cpuBugsInfo: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, cpuCollectorSubsystem, "bug_info"),
			"The `bugs` field of CPU information from /proc/cpuinfo taken from the first core.",
			[]string{"bug"}, nil,
		),
		cpuGuest: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, cpuCollectorSubsystem, "guest_seconds_total"),
			"Seconds the CPUs spent in guests (VMs) for each mode.",
			[]string{"cpu", "mode"}, nil,
		),
		cpuCoreThrottle: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, cpuCollectorSubsystem, "core_throttles_total"),
			"Number of times this CPU core has been throttled.",
			[]string{"package", "core"}, nil,
		),
		cpuPackageThrottle: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, cpuCollectorSubsystem, "package_throttles_total"),
			"Number of times this CPU package has been throttled.",
			[]string{"package"}, nil,
		),
		cpuIsolated: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, cpuCollectorSubsystem, "isolated"),
			"Whether each core is isolated, information from /sys/devices/system/cpu/isolated.",
			[]string{"cpu"}, nil,
		),
		isolatedCpus: isolcpus,
		cpuStats:     make(map[int64]procfs.CPUStat),
	}
	return c, nil
}

// Update implements Collector and exposes cpu related metrics from /proc/stat and /sys/.../cpu/.
func (c *cpuCollector) Update(ch chan<- prometheus.Metric) error {
	if err := c.updateInfo(ch); err != nil {
		return err
	}
	if err := c.updateStat(ch); err != nil {
		return err
	}
	if c.isolatedCpus != nil {
		c.updateIsolated(ch)
	}
	return c.updateThermalThrottle(ch)
}

// updateInfo reads /proc/cpuinfo
func (c *cpuCollector) updateInfo(ch chan<- prometheus.Metric) error {
	info, err := c.fs.CPUInfo()
	if err != nil {
		return err
	}
	for _, cpu := range info {
		ch <- prometheus.MustNewConstMetric(c.cpuInfo,
			prometheus.GaugeValue,
			1,
			cpu.PhysicalID,
			cpu.CoreID,
			strconv.Itoa(int(cpu.Processor)),
			cpu.VendorID,
			cpu.CPUFamily,
			cpu.Model,
			cpu.ModelName,
			cpu.Microcode,
			cpu.Stepping,
			cpu.CacheSize)
	}

	for _, cpu := range info {
		ch <- prometheus.MustNewConstMetric(c.cpuFrequencyHz,
			prometheus.GaugeValue,
			cpu.CPUMHz*1e6,
			cpu.PhysicalID,
			cpu.CoreID,
			strconv.Itoa(int(cpu.Processor)))
	}

	if len(info) != 0 {
		cpu := info[0]
		if err := updateFieldInfo(cpu.Flags, c.cpuFlagsInfo, ch); err != nil {
			return err
		}
		if err := updateFieldInfo(cpu.Bugs, c.cpuBugsInfo, ch); err != nil {
			return err
		}
	}

	return nil
}

func updateFieldInfo(valueList []string, desc *prometheus.Desc, ch chan<- prometheus.Metric) error {

	for _, val := range valueList {
		ch <- prometheus.MustNewConstMetric(desc,
			prometheus.GaugeValue,
			1,
			val,
		)
	}
	return nil
}

// updateThermalThrottle reads /sys/devices/system/cpu/cpu* and expose thermal throttle statistics.
func (c *cpuCollector) updateThermalThrottle(ch chan<- prometheus.Metric) error {
	cpus, err := filepath.Glob(sysFilePath("devices/system/cpu/cpu[0-9]*"))
	if err != nil {
		return err
	}

	packageThrottles := make(map[uint64]uint64)
	packageCoreThrottles := make(map[uint64]map[uint64]uint64)

	// cpu loop
	for _, cpu := range cpus {
		// See
		// https://www.kernel.org/doc/Documentation/x86/topology.txt
		// https://www.kernel.org/doc/Documentation/cputopology.txt
		// https://www.kernel.org/doc/Documentation/ABI/testing/sysfs-devices-system-cpu
		var err error
		var physicalPackageID, coreID uint64

		// topology/physical_package_id
		if physicalPackageID, err = readUintFromFile(filepath.Join(cpu, "topology", "physical_package_id")); err != nil {
			continue
		}
		// topology/core_id
		if coreID, err = readUintFromFile(filepath.Join(cpu, "topology", "core_id")); err != nil {
			continue
		}

		// metric node_cpu_core_throttles_total
		//
		// We process this metric before the package throttles as there
		// are CPU+kernel combinations that only present core throttles
		// but no package throttles.
		// Seen e.g. on an Intel Xeon E5472 system with RHEL 6.9 kernel.
		if _, present := packageCoreThrottles[physicalPackageID]; !present {
			packageCoreThrottles[physicalPackageID] = make(map[uint64]uint64)
		}
		if _, present := packageCoreThrottles[physicalPackageID][coreID]; !present {
			// Read thermal_throttle/core_throttle_count only once
			if coreThrottleCount, err := readUintFromFile(filepath.Join(cpu, "thermal_throttle", "core_throttle_count")); err == nil {
				packageCoreThrottles[physicalPackageID][coreID] = coreThrottleCount
			}
		}

		// metric node_cpu_package_throttles_total
		if _, present := packageThrottles[physicalPackageID]; !present {
			// Read thermal_throttle/package_throttle_count only once
			if packageThrottleCount, err := readUintFromFile(filepath.Join(cpu, "thermal_throttle", "package_throttle_count")); err == nil {
				packageThrottles[physicalPackageID] = packageThrottleCount
			}
		}
	}

	for physicalPackageID, packageThrottleCount := range packageThrottles {
		ch <- prometheus.MustNewConstMetric(c.cpuPackageThrottle,
			prometheus.CounterValue,
			float64(packageThrottleCount),
			strconv.FormatUint(physicalPackageID, 10))
	}

	for physicalPackageID, coreMap := range packageCoreThrottles {
		for coreID, coreThrottleCount := range coreMap {
			ch <- prometheus.MustNewConstMetric(c.cpuCoreThrottle,
				prometheus.CounterValue,
				float64(coreThrottleCount),
				strconv.FormatUint(physicalPackageID, 10),
				strconv.FormatUint(coreID, 10))
		}
	}
	return nil
}

// updateIsolated reads /sys/devices/system/cpu/isolated through sysfs and exports isolation level metrics.
func (c *cpuCollector) updateIsolated(ch chan<- prometheus.Metric) {
	for _, cpu := range c.isolatedCpus {
		cpuNum := strconv.Itoa(int(cpu))
		ch <- prometheus.MustNewConstMetric(c.cpuIsolated, prometheus.GaugeValue, 1.0, cpuNum)
	}
}

// updateStat reads /proc/stat through procfs and exports CPU-related metrics.
func (c *cpuCollector) updateStat(ch chan<- prometheus.Metric) error {
	stats, err := c.fs.Stat()
	if err != nil {
		return err
	}

	c.updateCPUStats(stats.CPU)

	// Acquire a lock to read the stats.
	c.cpuStatsMutex.Lock()
	defer c.cpuStatsMutex.Unlock()
	for cpuID, cpuStat := range c.cpuStats {
		cpuNum := strconv.Itoa(int(cpuID))
		ch <- prometheus.MustNewConstMetric(c.cpu, prometheus.CounterValue, cpuStat.User, cpuNum, "user")
		ch <- prometheus.MustNewConstMetric(c.cpu, prometheus.CounterValue, cpuStat.Nice, cpuNum, "nice")
		ch <- prometheus.MustNewConstMetric(c.cpu, prometheus.CounterValue, cpuStat.System, cpuNum, "system")
		ch <- prometheus.MustNewConstMetric(c.cpu, prometheus.CounterValue, cpuStat.Idle, cpuNum, "idle")
		ch <- prometheus.MustNewConstMetric(c.cpu, prometheus.CounterValue, cpuStat.Iowait, cpuNum, "iowait")
		ch <- prometheus.MustNewConstMetric(c.cpu, prometheus.CounterValue, cpuStat.IRQ, cpuNum, "irq")
		ch <- prometheus.MustNewConstMetric(c.cpu, prometheus.CounterValue, cpuStat.SoftIRQ, cpuNum, "softirq")
		ch <- prometheus.MustNewConstMetric(c.cpu, prometheus.CounterValue, cpuStat.Steal, cpuNum, "steal")

		// Guest CPU is also accounted for in cpuStat.User and cpuStat.Nice, expose these as separate metrics.
		ch <- prometheus.MustNewConstMetric(c.cpuGuest, prometheus.CounterValue, cpuStat.Guest, cpuNum, "user")
		ch <- prometheus.MustNewConstMetric(c.cpuGuest, prometheus.CounterValue, cpuStat.GuestNice, cpuNum, "nice")
	}

	return nil
}

// updateCPUStats updates the internal cache of CPU stats.
func (c *cpuCollector) updateCPUStats(newStats map[int64]procfs.CPUStat) {

	// Acquire a lock to update the stats.
	c.cpuStatsMutex.Lock()
	defer c.cpuStatsMutex.Unlock()

	// Reset the cache if the list of CPUs has changed.
	for i, n := range newStats {
		cpuStats := c.cpuStats[i]

		// If idle jumps backwards by more than X seconds, assume we had a hotplug event and reset the stats for this CPU.
		if (cpuStats.Idle - n.Idle) >= jumpBackSeconds {
			cpuStats = procfs.CPUStat{}
		}

		if n.Idle >= cpuStats.Idle {
			cpuStats.Idle = n.Idle
		}

		if n.User >= cpuStats.User {
			cpuStats.User = n.User
		}

		if n.Nice >= cpuStats.Nice {
			cpuStats.Nice = n.Nice
		}

		if n.System >= cpuStats.System {
			cpuStats.System = n.System
		}

		if n.Iowait >= cpuStats.Iowait {
			cpuStats.Iowait = n.Iowait
		}

		if n.IRQ >= cpuStats.IRQ {
			cpuStats.IRQ = n.IRQ
		}

		if n.SoftIRQ >= cpuStats.SoftIRQ {
			cpuStats.SoftIRQ = n.SoftIRQ
		}

		if n.Steal >= cpuStats.Steal {
			cpuStats.Steal = n.Steal
		}

		if n.Guest >= cpuStats.Guest {
			cpuStats.Guest = n.Guest
		}

		if n.GuestNice >= cpuStats.GuestNice {
			cpuStats.GuestNice = n.GuestNice
		}

		c.cpuStats[i] = cpuStats
	}

	// Remove offline CPUs.
	if len(newStats) != len(c.cpuStats) {
		onlineCPUIds := maps.Keys(newStats)
		maps.DeleteFunc(c.cpuStats, func(key int64, item procfs.CPUStat) bool {
			return !slices.Contains(onlineCPUIds, key)
		})
	}
}
