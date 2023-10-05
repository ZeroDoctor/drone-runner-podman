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
	}

	for _, sec := range step.Secrets {
		basic.EnvSecrets[sec.Env] = string(sec.Data)
	}

	volume := specgen.ContainerStorageConfig{
		Image:   step.Image,
		WorkDir: step.WorkingDir,
		ShmSize: toPtr(step.ShmSize),
	}

	volumeSet := toVolumeSet(spec, step)
	for path := range volumeSet {
		volume.Volumes = append(volume.Volumes, &specgen.NamedVolume{
			Dest: path,
		})
	}

	if len(step.Volumes) != 0 {
		volume.Devices = toLinuxDeviceSlice(spec, step)
		volume.Mounts = toLinuxVolumeMounts(spec, step)
	}

	security := specgen.ContainerSecurityConfig{
		User:       step.User,
		Privileged: step.Privileged,
	}

	// windows does not support privileged so we hard-code this value to false.
	// podman doesn't even support windows so this would be a problem
	// if we reach here
	if spec.Platform.OS == "windows" {
		security.Privileged = false
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

// helper function that converts a slice of volume paths to a set
// of unique volume names.
func toVolumeSet(spec *Spec, step *Step) map[string]struct{} {
	set := map[string]struct{}{}
	for _, mount := range step.Volumes {
		volume, ok := lookupVolume(spec, mount.Name)
		if !ok {
			continue
		}
		if isDevice(volume) {
			continue
		}
		if isNamedPipe(volume) {
			continue
		}
		if isBindMount(volume) == false {
			continue
		}
		set[mount.Path] = struct{}{}
	}
	return set
}

// helper function returns a slice of volume mounts.
func toVolumeSlice(spec *Spec, step *Step) []string {
	// this entire function should be deprecated in
	// favor of toVolumeMounts, however, I am unable
	// to get it working with data volumes.
	var to []string
	for _, mount := range step.Volumes {
		volume, ok := lookupVolume(spec, mount.Name)
		if !ok {
			continue
		}
		if isDevice(volume) {
			continue
		}
		if isDataVolume(volume) {
			path := volume.EmptyDir.ID + ":" + mount.Path
			to = append(to, path)
		}
		if isBindMount(volume) {
			path := volume.HostPath.Path + ":" + mount.Path
			to = append(to, path)
		}
	}
	return to
}

// helper function returns a slice of docker mount
// configurations.
func toLinuxVolumeMounts(spec *Spec, step *Step) []specs.Mount {
	var mounts []specs.Mount
	for _, target := range step.Volumes {
		source, ok := lookupVolume(spec, target.Name)
		if !ok {
			continue
		}

		if isBindMount(source) && !isDevice(source) {
			continue
		}

		// HACK: this condition can be removed once
		// toVolumeSlice has been fully replaced. at this
		// time, I cannot figure out how to get mounts
		// working with data volumes :(
		if isDataVolume(source) {
			continue
		}
		mounts = append(mounts, toLinuxMount(source, target))
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
		// to.ReadOnly = source.HostPath.ReadOnly
	}

	if isTempfs(source) {
		// NOTE: not sure if this is translatable
		//  probably part of resource struct

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
