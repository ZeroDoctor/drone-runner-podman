// Copyright 2019 Drone.IO Inc. All rights reserved.
// Use of this source code is governed by the Polyform License
// that can be found in the LICENSE file.

package engine

import (
	"bytes"
	"context"
	"io"
	"os"
	"time"

	"github.com/drone-runners/drone-runner-podman/internal/podman/errors"
	"github.com/drone-runners/drone-runner-podman/internal/podman/image"
	"github.com/drone-runners/drone-runner-podman/internal/podman/jsonmessage"
	"github.com/drone/runner-go/logger"
	"github.com/drone/runner-go/pipeline/runtime"

	"github.com/containers/common/libnetwork/types"
	"github.com/containers/podman/v4/pkg/bindings"
	"github.com/containers/podman/v4/pkg/bindings/containers"
	"github.com/containers/podman/v4/pkg/bindings/images"
	"github.com/containers/podman/v4/pkg/bindings/network"
	"github.com/containers/podman/v4/pkg/bindings/volumes"
	"github.com/containers/podman/v4/pkg/domain/entities"
)

// TODO: figure out what to do about this global
const UNIX_SOCK string = "unix:///run/podman/podman.sock"

// Opts configures the Docker engine.
type Opts struct {
	HidePull bool
}

// Podman implements a Podman pipeline engine.
type Podman struct {
	hidePull bool
	conn     context.Context
}

// New returns a new engine.
func New(conn context.Context, opts Opts) *Podman {
	return &Podman{
		hidePull: opts.HidePull,
	}
}

// NewEnv returns a new Engine from the environment.
func NewEnv(ctx context.Context, opts Opts) (*Podman, error) {
	conn, err := bindings.NewConnection(ctx, UNIX_SOCK)
	return New(conn, opts), err
}

// Setup the pipeline environment.
func (e *Podman) Setup(ctx context.Context, specv runtime.Spec) error {
	spec := specv.(*Spec)

	// creates the default temporary (local) volumes
	// that are mounted into each container step.
	for _, vol := range spec.Volumes {
		if vol.EmptyDir == nil {
			continue
		}

		_, err := volumes.Create(
			e.conn,
			entities.VolumeCreateOptions{
				Name:   vol.EmptyDir.ID,
				Driver: "local",
				Label:  vol.EmptyDir.Labels,
			},
			&volumes.CreateOptions{},
		)
		if err != nil {
			return errors.TrimExtraInfo(err)
		}
	}

	// creates the default pod network. All containers
	// defined in the pipeline are attached to this network.
	driver := "bridge"
	if spec.Platform.OS == "windows" {
		driver = "nat"
	}

	_, err := network.Create(e.conn, &types.Network{
		ID:      spec.Network.ID,
		Driver:  driver,
		Options: spec.Network.Options,
		Labels:  spec.Network.Labels,
	})

	// launches the inernal setup steps
	for _, step := range spec.Internal {
		if err := e.create(ctx, spec, step, io.Discard); err != nil {
			logger.FromContext(ctx).
				WithError(err).
				WithField("container", step.ID).
				Errorln("cannot create tmate container")
			return err
		}
		if err := e.start(ctx, step.ID); err != nil {
			logger.FromContext(ctx).
				WithError(err).
				WithField("container", step.ID).
				Errorln("cannot start tmate container")
			return err
		}
		if !step.Detach {
			// the internal containers perform short-lived tasks
			// and should not require > 1 minute to execute.
			//
			// just to be on the safe side we apply a timeout to
			// ensure we never block pipeline execution because we
			// are waiting on an internal task.
			ctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			e.wait(ctx, step.ID)
		}
	}

	return errors.TrimExtraInfo(err)
}

// Destroy the pipeline environment.
func (e *Podman) Destroy(ctx context.Context, specv runtime.Spec) error {
	spec := specv.(*Spec)

	removeOpts := containers.RemoveOptions{
		Force:   toPtr(true),
		Volumes: toPtr(true),
		Depend:  toPtr(true), // maybe?
		Ignore:  toPtr(true),
	}

	// stop all containers
	for _, step := range append(spec.Steps, spec.Internal...) {
		if err := containers.Kill(ctx, step.ID, &containers.KillOptions{Signal: toPtr("9")}); err != nil {
			logger.FromContext(ctx).
				WithError(err).
				WithField("container", step.ID).
				Debugln("cannot kill container")
		}
	}

	// cleanup all containers
	for _, step := range append(spec.Steps, spec.Internal...) {
		if _, err := containers.Remove(e.conn, step.ID, &removeOpts); err != nil {
			logger.FromContext(ctx).
				WithError(err).
				WithField("container", step.ID).
				Debugln("cannot remove container")
		}
	}

	// cleanup all volumes
	for _, vol := range spec.Volumes {
		if vol.EmptyDir == nil {
			continue
		}
		// tempfs volumes do not have a volume entry,
		// and therefore do not require removal.
		if vol.EmptyDir.Medium == "memory" {
			continue
		}

		if err := volumes.Remove(e.conn, vol.EmptyDir.ID, &volumes.RemoveOptions{Force: toPtr(true)}); err != nil {
			logger.FromContext(ctx).
				WithError(err).
				WithField("volume", vol.EmptyDir.ID).
				Debugln("cannot remove volume")
		}
	}

	if _, err := network.Remove(e.conn, spec.Network.ID, &network.RemoveOptions{}); err != nil {
		logger.FromContext(ctx).
			WithError(err).
			WithField("network", spec.Network.ID).
			Debugln("cannot remove network")
	}

	// notice that we never collect or return any errors.
	// this is because we silently ignore cleanup failures
	// and instead ask the system admin to periodically run
	// `docker prune` commands.
	return nil
}

// Run runs the pipeline step.
func (e *Podman) Run(ctx context.Context, specv runtime.Spec, stepv runtime.Step, output io.Writer) (*runtime.State, error) {
	spec := specv.(*Spec)
	step := stepv.(*Step)

	// create the container
	err := e.create(ctx, spec, step, output)
	if err != nil {
		return nil, errors.TrimExtraInfo(err)
	}
	// start the container
	err = e.start(ctx, step.ID)
	if err != nil {
		return nil, errors.TrimExtraInfo(err)
	}
	// this is an experimental feature that closes logging as the last step
	var allowDeferTailLog = os.Getenv("DRONE_DEFER_TAIL_LOG") == "true"
	if allowDeferTailLog {
		// tail the container
		logger.FromContext(ctx).
			WithField("step id", step.ID).
			Debugln("using deferred podman tail")
		logs, tailErr := e.deferTail(ctx, step.ID, output)
		if tailErr != nil {
			return nil, errors.TrimExtraInfo(tailErr)
		}
		defer logs.Close()
	} else {
		err = e.tail(ctx, step.ID, output)
		if err != nil {
			return nil, errors.TrimExtraInfo(err)
		}
	}
	// wait for the response
	return e.waitRetry(ctx, step.ID)
}

//
// emulate docker commands
//

func (e *Podman) create(ctx context.Context, spec *Spec, step *Step, output io.Writer) error {
	// create pull options with encoded authorization credentials.
	pullopts := images.PullOptions{}
	if step.Auth != nil {
		pullopts.Username = &step.Auth.Username
		pullopts.Password = &step.Auth.Password
	}

	// Read(p []byte) (n int, err error)
	// automatically pull the latest version of the image if requested
	// by the process configuration, or if the image is :latest
	if step.Pull == PullAlways ||
		(step.Pull == PullDefault && image.IsLatest(step.Image)) {
		rc, pullerr := images.Pull(e.conn, step.Image, &pullopts)
		if pullerr == nil {
			b := bytes.NewBuffer(flattenToBytes(rc))
			if e.hidePull {
				io.Copy(io.Discard, b)
			}

			jsonmessage.Copy(b, output)
		}
		if pullerr != nil {
			return pullerr
		}
	}

	_, err := containers.CreateWithSpec(e.conn, toSpec(spec, step), &containers.CreateOptions{})

	// automatically pull and try to re-create the image if the
	// failure is caused because the image does not exist.
	if step.Pull != PullNever {
		rc, pullerr := images.Pull(e.conn, step.Image, &pullopts)
		if pullerr != nil {
			return pullerr
		}

		b := bytes.NewBuffer(flattenToBytes(rc))
		if e.hidePull {
			io.Copy(io.Discard, b)
		}
		jsonmessage.Copy(b, output)

		// once the image is successfully pulled we attempt to
		// re-create the container.
		_, err = containers.CreateWithSpec(e.conn, toSpec(spec, step), &containers.CreateOptions{})
	}
	if err != nil {
		return err
	}

	// attach the container to user-defined networks.
	// primarily used to attach global user-defined networks.
	if step.Network == "" {
		for _, net := range step.Networks {
			err = network.Connect(e.conn, net, step.ID, &types.PerNetworkOptions{
				Aliases: []string{net},
			})
			if err != nil {
				return nil
			}
		}
	}

	return nil
}

// helper function emulates the `docker start` command.
func (e *Podman) start(ctx context.Context, id string) error {
	return containers.Start(e.conn, id, &containers.StartOptions{})
}

// helper function emulates the `docker wait` command, blocking
// until the container stops and returning the exit code.
func (e *Podman) waitRetry(ctx context.Context, id string) (*runtime.State, error) {
	for {
		// if the context is canceled, meaning the
		// pipeline timed out or was killed by the
		// end-user, we should exit with an error.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		state, err := e.wait(ctx, id)
		if err != nil {
			return nil, err
		}
		if state.Exited {
			return state, err
		}
		logger.FromContext(ctx).
			WithField("container", id).
			Trace("podman wait exited unexpectedly")
	}
}

// helper function emulates the `docker wait` command, blocking
// until the container stops and returning the exit code.
func (e *Podman) wait(ctx context.Context, id string) (*runtime.State, error) {
	containers.Wait(ctx, id, &containers.WaitOptions{
		Conditions: []string{"created", "exited", "dead", "removing", "removed"},
	})

	info, err := containers.Inspect(ctx, id, &containers.InspectOptions{})
	if err != nil {
		return nil, err
	}

	return &runtime.State{
		Exited:    !info.State.Running,
		ExitCode:  int(info.State.ExitCode),
		OOMKilled: info.State.OOMKilled,
	}, nil
}

// helper function emulates the `docker logs -f` command, streaming all container logs until the container stops.
func (e *Podman) deferTail(ctx context.Context, id string, output io.Writer) (logs io.ReadCloser, err error) {
	opts := containers.LogOptions{
		Follow:     toPtr(true),
		Stdout:     toPtr(true),
		Stderr:     toPtr(true),
		Timestamps: toPtr(false),
	}

	out := make(chan string, 512)
	error := make(chan string, 512)

	err = containers.Logs(ctx, id, &opts, out, error)
	if err != nil {
		logger.FromContext(ctx).
			WithError(err).
			WithField("container", id).
			Debugln("failed to stream logs")
		return nil, err
	}

	logs = NewChansReadClose(ctx, out, error)
	io.Copy(output, logs)

	return logs, nil
}

// helper function emulates the `docker logs -f` command, streaming all container logs until the container stops.
func (e *Podman) tail(ctx context.Context, id string, output io.Writer) error {
	opts := containers.LogOptions{
		Follow:     toPtr(true),
		Stdout:     toPtr(true),
		Stderr:     toPtr(true),
		Timestamps: toPtr(false),
	}

	out := make(chan string, 100)
	error := make(chan string, 100)

	err := containers.Logs(ctx, id, &opts, out, error)
	if err != nil {
		return err
	}

	go func() {
		logs := NewChansReadClose(ctx, out, error)
		io.Copy(output, logs)
		logs.Close()
	}()

	return nil
}
