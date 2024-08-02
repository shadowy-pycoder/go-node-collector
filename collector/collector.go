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
	"slices"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/exp/maps"
)

// Namespace defines the common namespace to be used by all metrics.
const namespace = "node"

var (
	scrapeDurationDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "scrape", "collector_duration_seconds"),
		"node_exporter: Duration of a collector scrape.",
		[]string{"collector"},
		nil,
	)
	scrapeSuccessDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "scrape", "collector_success"),
		"node_exporter: Whether a collector succeeded.",
		[]string{"collector"},
		nil,
	)
	factories      = make(map[string]func() (Collector, error))
	collectorState = make(map[string]bool)
)

func registerCollector(collector string, factory func() (Collector, error)) {
	factories[collector] = factory
	collectorState[collector] = true
}

// NodeCollector implements the prometheus.Collector interface.
type NodeCollector struct {
	collectors map[string]Collector
}

// NewNodeCollector creates a new NodeCollector.
func NewNodeCollector(filters ...string) (*NodeCollector, error) {
	f := make(map[string]bool)
	for _, filter := range filters {
		_, exist := collectorState[filter]
		if !exist {
			supportedColectors := maps.Keys(collectorState)
			slices.Sort(supportedColectors)
			return nil, fmt.Errorf("missing collector: %s. possible values: %s", filter, supportedColectors)
		}
		f[filter] = true
	}
	collectors := make(map[string]Collector)
	for key := range collectorState {
		if len(f) > 0 && !f[key] {
			continue
		}
		collector, err := factories[key]()
		if err != nil {
			return nil, err
		}
		collectors[key] = collector
	}
	return &NodeCollector{collectors: collectors}, nil
}

// Describe implements the prometheus.Collector interface.
func (n NodeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- scrapeDurationDesc
	ch <- scrapeSuccessDesc
}

// Collect implements the prometheus.Collector interface.
func (n NodeCollector) Collect(ch chan<- prometheus.Metric) {
	wg := sync.WaitGroup{}
	wg.Add(len(n.collectors))
	for name, c := range n.collectors {
		go func(name string, c Collector) {
			execute(name, c, ch)
			wg.Done()
		}(name, c)
	}
	wg.Wait()
}

func execute(name string, c Collector, ch chan<- prometheus.Metric) {
	begin := time.Now()
	err := c.Update(ch)
	duration := time.Since(begin)
	var success float64

	if err != nil {
		success = 0
	} else {
		success = 1
	}
	ch <- prometheus.MustNewConstMetric(scrapeDurationDesc, prometheus.GaugeValue, duration.Seconds(), name)
	ch <- prometheus.MustNewConstMetric(scrapeSuccessDesc, prometheus.GaugeValue, success, name)
}

// Collector is the interface a collector has to implement.
type Collector interface {
	// Get new metrics and expose them via prometheus registry.
	Update(ch chan<- prometheus.Metric) error
}
