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
	"path/filepath"

	"github.com/prometheus/procfs"
)

func procFilePath(name string) string {
	return filepath.Join(procfs.DefaultMountPoint, name)
}

func sysFilePath(name string) string {
	return filepath.Join("/sys", name)
}

func rootfsFilePath(name string) string {
	return filepath.Join("/", name)
}

func udevDataFilePath(name string) string {
	return filepath.Join("/run/udev/data", name)
}
