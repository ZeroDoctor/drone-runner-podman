// Copyright 2019 Drone.IO Inc. All rights reserved.
// Use of this source code is governed by the Polyform License
// that can be found in the LICENSE file.

package engine

import (
	"io/fs"
	"net"
	"strings"

	"github.com/containers/common/libnetwork/types"
	"github.com/containers/podman/v4/pkg/specgen"
	"github.com/docker/docker/api/types/mount"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/sirupsen/logrus"
)

func toSpec(spec *Spec, step *Step) *specgen.SpecGenerator {
	basic := specgen.ContainerBasicConfig{
		Name:         step.ID,
		RawImageName: step.Image,
		Labels:       step.Labels,
		Env:          step.Envs,
		Entrypoint:   step.Entrypoint,
		Command:      step.Command,
		Stdin:        false,
		Terminal:     false,
		EnvSecrets:   make(map[string]string, 0),
	}

	for _, sec := range step.Secrets {
		basic.Env[sec.Env] = string(sec.Data)
	}

	volume := specgen.ContainerStorageConfig{
		Image:            step.Image,
		WorkDir:          step.WorkingDir,
		CreateWorkingDir: true,
		ShmSize:          toPtr(step.ShmSize),
	}

	if len(step.Volumes) != 0 {
		volume.Devices = toLinuxDeviceSlice(spec, step)
		volume.Mounts = toLinuxVolumeMounts(spec, step)
		volume.Volumes = toLinuxVolumeSlice(spec, step)
	}

	logrus.Tracef("[has_privileged=%+v]", step.Privileged)
	security := specgen.ContainerSecurityConfig{
		User:       step.User,
		Privileged: step.Privileged,
	}

	var dns []net.IP
	for i := range step.DNS {
		ip, _, err := net.ParseCIDR(step.DNS[i])
		if err != nil {
			logrus.Warnf("failed to parse dns [ip=%s] [error=%s]", step.DNS[i], err.Error())
			continue
		}
		dns = append(dns, ip)
	}

	net := specgen.ContainerNetworkConfig{
		DNSServers: dns,
		DNSSearch:  step.DNSSearch,
		HostAdd:    step.ExtraHosts,
	}

	if len(step.Network) > 0 {
		net.Networks = make(map[string]types.PerNetworkOptions)
		net.Networks[step.Network] = types.PerNetworkOptions{}
	}

	resource := specgen.ContainerResourceConfig{}
	if isUnlimited(step) == false {
		resource = specgen.ContainerResourceConfig{
			CPUPeriod: uint64(step.CPUPeriod),
			CPUQuota:  step.CPUQuota,
			ResourceLimits: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Cpus:   strings.Join(step.CPUSet, ","),
					Shares: toPtr(uint64(step.CPUShares)),
				},
				Memory: &specs.LinuxMemory{
					Limit: &step.MemLimit,
					Swap:  &step.MemSwapLimit,
				},
			},
		}
	}

	config := &specgen.SpecGenerator{
		ContainerBasicConfig:    basic,
		ContainerStorageConfig:  volume,
		ContainerSecurityConfig: security,
		ContainerNetworkConfig:  net,
		ContainerResourceConfig: resource,
	}

	logrus.Tracef("creating [config=%+v]", config)
	return config
}

// helper function that converts a slice of device paths to a slice of
// container.DeviceMapping.
func toLinuxDeviceSlice(spec *Spec, step *Step) []specs.LinuxDevice {
	var to []specs.LinuxDevice
	for _, mount := range step.Devices {
		device, ok := lookupVolume(spec, mount.Name)
		if !ok {
			continue
		}
		if isDevice(device) == false {
			continue
		}
		to = append(to, specs.LinuxDevice{
			// NOTE: there only host path... weird
			Path: device.HostPath.Path,

			// PathOnHost:      device.HostPath.Path,
			// PathInContainer: mount.DevicePath,
			FileMode: toPtr(fs.ModePerm),
		})
	}
	if len(to) == 0 {
		return nil
	}
	return to
}

// helper function returns a slice of volume mounts.
func toLinuxVolumeSlice(spec *Spec, step *Step) []*specgen.NamedVolume {
	var to []*specgen.NamedVolume

	for _, mount := range step.Volumes {
		volume, ok := lookupVolume(spec, mount.Name)
		if !ok || isDevice(volume) || isBindMount(volume) {
			continue
		}
		if isDataVolume(volume) {
			to = append(to, &specgen.NamedVolume{
				Name: volume.EmptyDir.ID,
				Dest: mount.Path,
			})
		}

		logrus.Tracef("named [host=%+v] [empty=%+v] [mount=%+v]", volume.HostPath, volume.EmptyDir, mount)
	}

	return to
}

// helper function returns a slice of docker mount
// configurations.
func toLinuxVolumeMounts(spec *Spec, step *Step) []specs.Mount {
	var mounts []specs.Mount
	for _, mount := range step.Volumes {
		volume, ok := lookupVolume(spec, mount.Name)
		if !ok {
			continue
		}

		if isDataVolume(volume) {
			continue
		}

		mounts = append(mounts, toLinuxMount(volume, mount))
		logrus.Tracef("mount [host=%+v] [empty=%+v] [target=%+v]", volume.HostPath, volume.EmptyDir, mount)
	}
	if len(mounts) == 0 {
		return nil
	}
	return mounts
}

// helper function converts the volume declaration to a
// docker mount structure.
func toLinuxMount(source *Volume, target *VolumeMount) specs.Mount {
	to := specs.Mount{
		Destination: target.Path,
		Type:        string(toVolumeType(source)),
	}
	if isBindMount(source) || isNamedPipe(source) {
		to.Source = source.HostPath.Path
		if source.HostPath.ReadOnly {
			// options defaults = rw, suid, dev, exec, auto, nouser, and async
			to.Options = append(to.Options, "ro")
		}
	}

	if isTempfs(source) {
		// NOTE: specs.Mount might not be the right structure
		//	maybe ImageVolume is suitable here

		// to.TmpfsOptions = &mount.TmpfsOptions{
		// 	SizeBytes: source.EmptyDir.SizeLimit,
		// 	Mode:      0700,
		// }
	}

	return to
}

// helper function returns the docker volume enumeration
// for the given volume.
func toVolumeType(from *Volume) mount.Type {
	switch {
	case isDataVolume(from):
		return mount.TypeVolume
	case isTempfs(from):
		return mount.TypeTmpfs
	case isNamedPipe(from):
		return mount.TypeNamedPipe
	default:
		return mount.TypeBind
	}
}

// helper function that converts a key value map of
// environment variables to a string slice in key=value
// format.
func toEnv(env map[string]string) []string {
	var envs []string
	for k, v := range env {
		if v != "" {
			envs = append(envs, k+"="+v)
		}
	}
	return envs
}

// returns true if the container has no resource limits.
func isUnlimited(res *Step) bool {
	return len(res.CPUSet) == 0 &&
		res.CPUPeriod == 0 &&
		res.CPUQuota == 0 &&
		res.CPUShares == 0 &&
		res.MemLimit == 0 &&
		res.MemSwapLimit == 0
}

// returns true if the volume is a bind mount.
func isBindMount(volume *Volume) bool {
	return volume.HostPath != nil
}

// returns true if the volume is in-memory.
func isTempfs(volume *Volume) bool {
	return volume.EmptyDir != nil && volume.EmptyDir.Medium == "memory"
}

// returns true if the volume is a data-volume.
func isDataVolume(volume *Volume) bool {
	return volume.EmptyDir != nil && volume.EmptyDir.Medium != "memory"
}

// returns true if the volume is a device
func isDevice(volume *Volume) bool {
	return volume.HostPath != nil && strings.HasPrefix(volume.HostPath.Path, "/dev/")
}

// returns true if the volume is a named pipe.
func isNamedPipe(volume *Volume) bool {
	return volume.HostPath != nil &&
		strings.HasPrefix(volume.HostPath.Path, `\\.\pipe\`)
}

// helper function returns the named volume.
func lookupVolume(spec *Spec, name string) (*Volume, bool) {
	for _, v := range spec.Volumes {
		if v.HostPath != nil && v.HostPath.Name == name {
			return v, true
		}
		if v.EmptyDir != nil && v.EmptyDir.Name == name {
			return v, true
		}
	}
	return nil, false
}
