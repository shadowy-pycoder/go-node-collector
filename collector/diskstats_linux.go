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
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/procfs/blockdevice"
)

const (
	secondsPerTick = 1.0 / 1000.0

	// Read sectors and write sectors are the "standard UNIX 512-byte sectors, not any device- or filesystem-specific block size."
	// See also https://www.kernel.org/doc/Documentation/block/stat.txt
	unixSectorSize = 512.0

	// See udevadm(8).
	udevDevicePropertyPrefix = "E:"

	// Udev device properties.
	udevDMLVLayer               = "DM_LV_LAYER"
	udevDMLVName                = "DM_LV_NAME"
	udevDMName                  = "DM_NAME"
	udevDMUUID                  = "DM_UUID"
	udevDMVGName                = "DM_VG_NAME"
	udevIDATA                   = "ID_ATA"
	udevIDATARotationRateRPM    = "ID_ATA_ROTATION_RATE_RPM"
	udevIDATASATA               = "ID_ATA_SATA"
	udevIDATASATASignalRateGen1 = "ID_ATA_SATA_SIGNAL_RATE_GEN1"
	udevIDATASATASignalRateGen2 = "ID_ATA_SATA_SIGNAL_RATE_GEN2"
	udevIDATAWriteCache         = "ID_ATA_WRITE_CACHE"
	udevIDATAWriteCacheEnabled  = "ID_ATA_WRITE_CACHE_ENABLED"
	udevIDFSType                = "ID_FS_TYPE"
	udevIDFSUsage               = "ID_FS_USAGE"
	udevIDFSUUID                = "ID_FS_UUID"
	udevIDFSVersion             = "ID_FS_VERSION"
	udevIDModel                 = "ID_MODEL"
	udevIDPath                  = "ID_PATH"
	udevIDRevision              = "ID_REVISION"
	udevIDSerialShort           = "ID_SERIAL_SHORT"
	udevIDWWN                   = "ID_WWN"
	udevSCSIIdentSerial         = "SCSI_IDENT_SERIAL"
)

const diskSubsystem = "disk"

var (
	diskLabelNames = []string{"device"}

	readsCompletedDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, diskSubsystem, "reads_completed_total"),
		"The total number of reads completed successfully.",
		diskLabelNames, nil,
	)

	readBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, diskSubsystem, "read_bytes_total"),
		"The total number of bytes read successfully.",
		diskLabelNames, nil,
	)

	writesCompletedDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, diskSubsystem, "writes_completed_total"),
		"The total number of writes completed successfully.",
		diskLabelNames, nil,
	)

	writtenBytesDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, diskSubsystem, "written_bytes_total"),
		"The total number of bytes written successfully.",
		diskLabelNames, nil,
	)

	ioTimeSecondsDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, diskSubsystem, "io_time_seconds_total"),
		"Total seconds spent doing I/Os.",
		diskLabelNames, nil,
	)

	readTimeSecondsDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, diskSubsystem, "read_time_seconds_total"),
		"The total number of seconds spent by all reads.",
		diskLabelNames,
		nil,
	)

	writeTimeSecondsDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, diskSubsystem, "write_time_seconds_total"),
		"This is the total number of seconds spent by all writes.",
		diskLabelNames,
		nil,
	)
)

type typedFactorDesc struct {
	desc      *prometheus.Desc
	valueType prometheus.ValueType
}

type udevInfo map[string]string

func (d *typedFactorDesc) mustNewConstMetric(value float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d.desc, d.valueType, value, labels...)
}

func init() {
	registerCollector("diskstats", newDiskstatsCollector)
}

type diskstatsCollector struct {
	fs                      blockdevice.FS
	infoDesc                typedFactorDesc
	descs                   []typedFactorDesc
	filesystemInfoDesc      typedFactorDesc
	deviceMapperInfoDesc    typedFactorDesc
	ataDescs                map[string]typedFactorDesc
	getUdevDeviceProperties func(uint32, uint32) (udevInfo, error)
}

// newDiskstatsCollector returns a new Collector exposing disk device stats.
// Docs from https://www.kernel.org/doc/Documentation/iostats.txt
func newDiskstatsCollector() (Collector, error) {
	var diskLabelNames = []string{"device"}
	fs, err := blockdevice.NewDefaultFS()
	if err != nil {
		return nil, fmt.Errorf("failed to open sysfs: %w", err)
	}

	collector := diskstatsCollector{
		fs: fs,
		infoDesc: typedFactorDesc{
			desc: prometheus.NewDesc(prometheus.BuildFQName(namespace, diskSubsystem, "info"),
				"Info of /sys/block/<block_device>.",
				[]string{"device", "major", "minor", "path", "wwn", "model", "serial", "revision"},
				nil,
			), valueType: prometheus.GaugeValue,
		},
		descs: []typedFactorDesc{
			{
				desc: readsCompletedDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "reads_merged_total"),
					"The total number of reads merged.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: readBytesDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: readTimeSecondsDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: writesCompletedDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "writes_merged_total"),
					"The number of writes merged.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: writtenBytesDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: writeTimeSecondsDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "io_now"),
					"The number of I/Os currently in progress.",
					diskLabelNames,
					nil,
				), valueType: prometheus.GaugeValue,
			},
			{
				desc: ioTimeSecondsDesc, valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "io_time_weighted_seconds_total"),
					"The weighted # of seconds spent doing I/Os.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "discards_completed_total"),
					"The total number of discards completed successfully.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "discards_merged_total"),
					"The total number of discards merged.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "discarded_sectors_total"),
					"The total number of sectors discarded successfully.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "discard_time_seconds_total"),
					"This is the total number of seconds spent by all discards.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "flush_requests_total"),
					"The total number of flush requests completed successfully",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
			{
				desc: prometheus.NewDesc(
					prometheus.BuildFQName(namespace, diskSubsystem, "flush_requests_time_seconds_total"),
					"This is the total number of seconds spent by all flush requests.",
					diskLabelNames,
					nil,
				), valueType: prometheus.CounterValue,
			},
		},
		filesystemInfoDesc: typedFactorDesc{
			desc: prometheus.NewDesc(prometheus.BuildFQName(namespace, diskSubsystem, "filesystem_info"),
				"Info about disk filesystem.",
				[]string{"device", "type", "usage", "uuid", "version"},
				nil,
			), valueType: prometheus.GaugeValue,
		},
		deviceMapperInfoDesc: typedFactorDesc{
			desc: prometheus.NewDesc(prometheus.BuildFQName(namespace, diskSubsystem, "device_mapper_info"),
				"Info about disk device mapper.",
				[]string{"device", "name", "uuid", "vg_name", "lv_name", "lv_layer"},
				nil,
			), valueType: prometheus.GaugeValue,
		},
		ataDescs: map[string]typedFactorDesc{
			udevIDATAWriteCache: {
				desc: prometheus.NewDesc(prometheus.BuildFQName(namespace, diskSubsystem, "ata_write_cache"),
					"ATA disk has a write cache.",
					[]string{"device"},
					nil,
				), valueType: prometheus.GaugeValue,
			},
			udevIDATAWriteCacheEnabled: {
				desc: prometheus.NewDesc(prometheus.BuildFQName(namespace, diskSubsystem, "ata_write_cache_enabled"),
					"ATA disk has its write cache enabled.",
					[]string{"device"},
					nil,
				), valueType: prometheus.GaugeValue,
			},
			udevIDATARotationRateRPM: {
				desc: prometheus.NewDesc(prometheus.BuildFQName(namespace, diskSubsystem, "ata_rotation_rate_rpm"),
					"ATA disk rotation rate in RPMs (0 for SSDs).",
					[]string{"device"},
					nil,
				), valueType: prometheus.GaugeValue,
			},
		},
	}

	collector.getUdevDeviceProperties = getUdevDeviceProperties
	return &collector, nil
}

func (c *diskstatsCollector) Update(ch chan<- prometheus.Metric) error {
	diskStats, err := c.fs.ProcDiskstats()
	if err != nil {
		return fmt.Errorf("couldn't get diskstats: %w", err)
	}

	for _, stats := range diskStats {
		dev := stats.DeviceName

		info, _ := getUdevDeviceProperties(stats.MajorNumber, stats.MinorNumber)

		// This is usually the serial printed on the disk label.
		serial := info[udevSCSIIdentSerial]

		// If it's undefined, fallback to ID_SERIAL_SHORT instead.
		if serial == "" {
			serial = info[udevIDSerialShort]
		}

		ch <- c.infoDesc.mustNewConstMetric(1.0, dev,
			fmt.Sprint(stats.MajorNumber),
			fmt.Sprint(stats.MinorNumber),
			info[udevIDPath],
			info[udevIDWWN],
			info[udevIDModel],
			serial,
			info[udevIDRevision],
		)

		statCount := stats.IoStatsCount - 3 // Total diskstats record count, less MajorNumber, MinorNumber and DeviceName

		for i, val := range []float64{
			float64(stats.ReadIOs),
			float64(stats.ReadMerges),
			float64(stats.ReadSectors) * unixSectorSize,
			float64(stats.ReadTicks) * secondsPerTick,
			float64(stats.WriteIOs),
			float64(stats.WriteMerges),
			float64(stats.WriteSectors) * unixSectorSize,
			float64(stats.WriteTicks) * secondsPerTick,
			float64(stats.IOsInProgress),
			float64(stats.IOsTotalTicks) * secondsPerTick,
			float64(stats.WeightedIOTicks) * secondsPerTick,
			float64(stats.DiscardIOs),
			float64(stats.DiscardMerges),
			float64(stats.DiscardSectors),
			float64(stats.DiscardTicks) * secondsPerTick,
			float64(stats.FlushRequestsCompleted),
			float64(stats.TimeSpentFlushing) * secondsPerTick,
		} {
			if i >= statCount {
				break
			}
			ch <- c.descs[i].mustNewConstMetric(val, dev)
		}

		if fsType := info[udevIDFSType]; fsType != "" {
			ch <- c.filesystemInfoDesc.mustNewConstMetric(1.0, dev,
				fsType,
				info[udevIDFSUsage],
				info[udevIDFSUUID],
				info[udevIDFSVersion],
			)
		}

		if name := info[udevDMName]; name != "" {
			ch <- c.deviceMapperInfoDesc.mustNewConstMetric(1.0, dev,
				name,
				info[udevDMUUID],
				info[udevDMVGName],
				info[udevDMLVName],
				info[udevDMLVLayer],
			)
		}

		if ata := info[udevIDATA]; ata != "" {
			for attr, desc := range c.ataDescs {
				str, ok := info[attr]
				if !ok {
					continue
				}

				if value, err := strconv.ParseFloat(str, 64); err == nil {
					ch <- desc.mustNewConstMetric(value, dev)
				}
			}
		}
	}
	return nil
}

func getUdevDeviceProperties(major, minor uint32) (udevInfo, error) {
	filename := udevDataFilePath(fmt.Sprintf("b%d:%d", major, minor))

	data, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer data.Close()

	info := make(udevInfo)

	scanner := bufio.NewScanner(data)
	for scanner.Scan() {
		line := scanner.Text()

		// We're only interested in device properties.
		if !strings.HasPrefix(line, udevDevicePropertyPrefix) {
			continue
		}

		line = strings.TrimPrefix(line, udevDevicePropertyPrefix)

		/* TODO: After we drop support for Go 1.17, the condition below can be simplified to:

		if name, value, found := strings.Cut(line, "="); found {
			info[name] = value
		}
		*/
		if fields := strings.SplitN(line, "=", 2); len(fields) == 2 {
			info[fields[0]] = fields[1]
		}
	}

	return info, nil
}
