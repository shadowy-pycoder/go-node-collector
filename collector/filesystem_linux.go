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
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sys/unix"
)

var (
	mountTimeout         = time.Duration(5 * time.Second)
	statWorkerCount      = 4
	stuckMounts          = make(map[string]struct{})
	stuckMountsMtx       = &sync.Mutex{}
	filesystemLabelNames = []string{"device", "mountpoint", "fstype", "device_error"}
)

type filesystemLabels struct {
	device, mountPoint, fsType, options, deviceError string
}

type filesystemStats struct {
	labels            filesystemLabels
	size, free, avail float64
	files, filesFree  float64
	ro, deviceError   float64
}

func init() {
	registerCollector("filesystem", newFilesystemCollector)
}

type filesystemCollector struct {
	sizeDesc, freeDesc, availDesc *prometheus.Desc
	filesDesc, filesFreeDesc      *prometheus.Desc
	roDesc, deviceErrorDesc       *prometheus.Desc
}

// newFilesystemCollector returns a new Collector exposing filesystems stats.
func newFilesystemCollector() (Collector, error) {

	subsystem := "filesystem"

	sizeDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, subsystem, "size_bytes"),
		"Filesystem size in bytes.",
		filesystemLabelNames, nil,
	)

	freeDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, subsystem, "free_bytes"),
		"Filesystem free space in bytes.",
		filesystemLabelNames, nil,
	)

	availDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, subsystem, "avail_bytes"),
		"Filesystem space available to non-root users in bytes.",
		filesystemLabelNames, nil,
	)

	filesDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, subsystem, "files"),
		"Filesystem total file nodes.",
		filesystemLabelNames, nil,
	)

	filesFreeDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, subsystem, "files_free"),
		"Filesystem total free file nodes.",
		filesystemLabelNames, nil,
	)

	roDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, subsystem, "readonly"),
		"Filesystem read-only status.",
		filesystemLabelNames, nil,
	)

	deviceErrorDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, subsystem, "device_error"),
		"Whether an error occurred while getting statistics for the given device.",
		filesystemLabelNames, nil,
	)

	return &filesystemCollector{
		sizeDesc:        sizeDesc,
		freeDesc:        freeDesc,
		availDesc:       availDesc,
		filesDesc:       filesDesc,
		filesFreeDesc:   filesFreeDesc,
		roDesc:          roDesc,
		deviceErrorDesc: deviceErrorDesc,
	}, nil
}

func (c *filesystemCollector) Update(ch chan<- prometheus.Metric) error {
	stats, err := c.GetStats()
	if err != nil {
		return err
	}
	// Make sure we expose a metric once, even if there are multiple mounts
	seen := map[filesystemLabels]bool{}
	for _, s := range stats {
		if seen[s.labels] {
			continue
		}
		seen[s.labels] = true

		ch <- prometheus.MustNewConstMetric(
			c.deviceErrorDesc, prometheus.GaugeValue,
			s.deviceError, s.labels.device, s.labels.mountPoint, s.labels.fsType, s.labels.deviceError,
		)
		ch <- prometheus.MustNewConstMetric(
			c.roDesc, prometheus.GaugeValue,
			s.ro, s.labels.device, s.labels.mountPoint, s.labels.fsType, s.labels.deviceError,
		)

		if s.deviceError > 0 {
			continue
		}

		ch <- prometheus.MustNewConstMetric(
			c.sizeDesc, prometheus.GaugeValue,
			s.size, s.labels.device, s.labels.mountPoint, s.labels.fsType, s.labels.deviceError,
		)
		ch <- prometheus.MustNewConstMetric(
			c.freeDesc, prometheus.GaugeValue,
			s.free, s.labels.device, s.labels.mountPoint, s.labels.fsType, s.labels.deviceError,
		)
		ch <- prometheus.MustNewConstMetric(
			c.availDesc, prometheus.GaugeValue,
			s.avail, s.labels.device, s.labels.mountPoint, s.labels.fsType, s.labels.deviceError,
		)
		ch <- prometheus.MustNewConstMetric(
			c.filesDesc, prometheus.GaugeValue,
			s.files, s.labels.device, s.labels.mountPoint, s.labels.fsType, s.labels.deviceError,
		)
		ch <- prometheus.MustNewConstMetric(
			c.filesFreeDesc, prometheus.GaugeValue,
			s.filesFree, s.labels.device, s.labels.mountPoint, s.labels.fsType, s.labels.deviceError,
		)
	}
	return nil
}

// GetStats returns filesystem stats.
func (c *filesystemCollector) GetStats() ([]filesystemStats, error) {
	mps, err := mountPointDetails()
	if err != nil {
		return nil, err
	}
	stats := []filesystemStats{}
	labelChan := make(chan filesystemLabels)
	statChan := make(chan filesystemStats)
	wg := sync.WaitGroup{}

	workerCount := statWorkerCount
	if workerCount < 1 {
		workerCount = 1
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for labels := range labelChan {
				statChan <- c.processStat(labels)
			}
		}()
	}

	go func() {
		for _, labels := range mps {

			stuckMountsMtx.Lock()
			if _, ok := stuckMounts[labels.mountPoint]; ok {
				labels.deviceError = "mountpoint timeout"
				stats = append(stats, filesystemStats{
					labels:      labels,
					deviceError: 1,
				})
				stuckMountsMtx.Unlock()
				continue
			}

			stuckMountsMtx.Unlock()
			labelChan <- labels
		}
		close(labelChan)
		wg.Wait()
		close(statChan)
	}()

	for stat := range statChan {
		stats = append(stats, stat)
	}
	return stats, nil
}

func (c *filesystemCollector) processStat(labels filesystemLabels) filesystemStats {
	var ro float64
	for _, option := range strings.Split(labels.options, ",") {
		if option == "ro" {
			ro = 1
			break
		}
	}

	success := make(chan struct{})
	go stuckMountWatcher(labels.mountPoint, success)

	buf := new(unix.Statfs_t)
	err := unix.Statfs(rootfsFilePath(labels.mountPoint), buf)
	stuckMountsMtx.Lock()
	close(success)

	// If the mount has been marked as stuck, unmark it and log it's recovery.
	delete(stuckMounts, labels.mountPoint)
	stuckMountsMtx.Unlock()

	if err != nil {
		labels.deviceError = err.Error()
		return filesystemStats{
			labels:      labels,
			deviceError: 1,
			ro:          ro,
		}
	}

	return filesystemStats{
		labels:    labels,
		size:      float64(buf.Blocks) * float64(buf.Bsize),
		free:      float64(buf.Bfree) * float64(buf.Bsize),
		avail:     float64(buf.Bavail) * float64(buf.Bsize),
		files:     float64(buf.Files),
		filesFree: float64(buf.Ffree),
		ro:        ro,
	}
}

// stuckMountWatcher listens on the given success channel and if the channel closes
// then the watcher does nothing. If instead the timeout is reached, the
// mount point that is being watched is marked as stuck.
func stuckMountWatcher(mountPoint string, success chan struct{}) {
	mountCheckTimer := time.NewTimer(mountTimeout)
	defer mountCheckTimer.Stop()
	select {
	case <-success:
		// Success
	case <-mountCheckTimer.C:
		// Timed out, mark mount as stuck
		stuckMountsMtx.Lock()
		select {
		case <-success:
			// Success came in just after the timeout was reached, don't label the mount as stuck
		default:
			stuckMounts[mountPoint] = struct{}{}
		}
		stuckMountsMtx.Unlock()
	}
}

func mountPointDetails() ([]filesystemLabels, error) {
	file, err := os.Open(procFilePath("1/mounts"))
	if errors.Is(err, os.ErrNotExist) {
		file, err = os.Open(procFilePath("mounts"))
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return parseFilesystemLabels(file)
}

func parseFilesystemLabels(r io.Reader) ([]filesystemLabels, error) {
	var filesystems []filesystemLabels

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())

		if len(parts) < 4 {
			return nil, fmt.Errorf("malformed mount point information: %q", scanner.Text())
		}

		// Ensure we handle the translation of \040 and \011
		// as per fstab(5).
		parts[1] = strings.Replace(parts[1], "\\040", " ", -1)
		parts[1] = strings.Replace(parts[1], "\\011", "\t", -1)

		filesystems = append(filesystems, filesystemLabels{
			device:      parts[0],
			mountPoint:  parts[1],
			fsType:      parts[2],
			options:     parts[3],
			deviceError: "",
		})
	}

	return filesystems, scanner.Err()
}
