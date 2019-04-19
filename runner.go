package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/network"
	"go.coder.com/sail/internal/dockutil"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/strslice"
	"golang.org/x/xerrors"
)

// Docker labels for sail state.
const (
	baseImageLabel       = sailLabel + ".base_image"
	hatLabel             = sailLabel + ".hat"
	projectLocalDirLabel = sailLabel + ".project_local_dir"
	projectDirLabel      = sailLabel + ".project_dir"
	projectNameLabel     = sailLabel + ".project_name"
)

// runner holds all the information needed to assemble a new sail container.
// The runner stores itself as state on the container.
// It enables quick iteration on a container with small modifications to it's config.
// All mounts should be configured from the image.
type runner struct {
	cntName     string
	projectName string

	hostname string

	projectLocalDir string

	// hostUser is the uid on the host which is mapped to
	// the container's "user" user.
	hostUser string

	network string
	ip      string

	testCmd string
}

// runContainer creates and runs a new container.
// It handles installing code-server, and uses code-server as
// the container's root process.
// We want code-server to be the root process as it gives us the nice guarantee that
// the container is only online when code-server is working.
func (r *runner) runContainer(image string) error {
	cli := dockerClient()
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*30)
	defer cancel()

	var (
		err    error
		mounts []mount.Mount
	)

	mounts, err = r.mounts(mounts, image)
	if err != nil {
		return xerrors.Errorf("failed to assemble mounts: %w", err)
	}

	projectDir, err := r.projectDir(image)
	if err != nil {
		return err
	}

	// We want the code-server logs to be available inside the container for easy
	// access during development, but also going to stdout so `docker logs` can be used
	// to debug a failed code-server startup.
	cmd := "cd " + projectDir + "; code-server --data-dir ~/.config/Code --extensions-dir ~/.vscode/extensions --allow-http --no-auth 2>&1 | tee " + containerLogPath
	if r.testCmd != "" {
		cmd = r.testCmd + "; exit 1"
	}

	containerConfig := &container.Config{
		Hostname: r.hostname,
		Cmd: strslice.StrSlice{
			"bash", "-c", cmd,
		},
		Image: image,
		Labels: map[string]string{
			sailLabel:            "",
			projectDirLabel:      projectDir,
			projectLocalDirLabel: r.projectLocalDir,
			projectNameLabel:     r.projectName,
		},
		User: r.hostUser + ":user",
	}

	err = r.addImageDefinedLabels(image, containerConfig.Labels)
	if err != nil {
		return xerrors.Errorf("failed to add image defined labels: %w", err)
	}

	hostConfig := &container.HostConfig{
		Mounts:     mounts,
		Privileged: true,
	}

	netConfig := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			r.network: &network.EndpointSettings{
				NetworkID: r.network,
				IPAddress: r.ip,
			},
		},
	}

	_, err = cli.ContainerCreate(ctx, containerConfig, hostConfig, netConfig, r.cntName)
	if err != nil {
		return xerrors.Errorf("failed to create container: %w", err)
	}

	err = cli.ContainerStart(ctx, r.cntName, types.ContainerStartOptions{})
	if err != nil {
		return xerrors.Errorf("failed to start container: %w", err)
	}

	return nil
}

func (r *runner) mounts(mounts []mount.Mount, image string) ([]mount.Mount, error) {
	// Mount in VS Code configs.
	mounts = append(mounts, mount.Mount{
		Type:   "bind",
		Source: "~/.config/Code",
		Target: "~/.config/Code",
	})
	mounts = append(mounts, mount.Mount{
		Type:   "bind",
		Source: "~/.vscode/extensions",
		Target: "~/.vscode/extensions",
	})

	localGlobalStorageDir := filepath.Join(metaRoot(), r.cntName, "globalStorage")
	err := os.MkdirAll(localGlobalStorageDir, 0750)
	if err != nil {
		return nil, err
	}

	// globalStorage holds the UI state, and other code-server specific
	// state.
	mounts = append(mounts, mount.Mount{
		Type:   "bind",
		Source: localGlobalStorageDir,
		Target: "~/.local/share/code-server/globalStorage/",
	})

	projectDir, err := r.projectDir(image)
	if err != nil {
		return nil, err
	}

	mounts = append(mounts, mount.Mount{
		Type:   "bind",
		Source: r.projectLocalDir,
		Target: projectDir,
	})

	// Mount in code-server
	codeServerBinPath, err := loadCodeServer(context.Background())
	if err != nil {
		return nil, xerrors.Errorf("failed to load code-server: %w", err)
	}
	mounts = append(mounts, mount.Mount{
		Type:   mount.TypeBind,
		Source: codeServerBinPath,
		Target: "/usr/bin/code-server",
	})

	// We take the mounts from the final image so that it includes the hat and the baseImage.
	mounts, err = r.imageDefinedMounts(image, mounts)
	if err != nil {
		return nil, err
	}

	r.resolveMounts(mounts)
	return mounts, nil
}

// imageDefinedMounts adds a list of shares to the shares map from the image.
func (r *runner) imageDefinedMounts(image string, mounts []mount.Mount) ([]mount.Mount, error) {
	cli := dockerClient()
	defer cli.Close()

	ins, _, err := cli.ImageInspectWithRaw(context.Background(), image)
	if err != nil {
		return nil, xerrors.Errorf("failed to inspect %v: %w", image, err)
	}

	for k, v := range ins.ContainerConfig.Labels {
		const prefix = "share."
		if !strings.HasPrefix(k, prefix) {
			continue
		}

		tokens := strings.Split(v, ":")
		if len(tokens) != 2 {
			return nil, xerrors.Errorf("invalid share %q", v)
		}

		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: tokens[0],
			Target: tokens[1],
		})
	}
	return mounts, nil
}

// addImageDefinedLabels adds any sail labels that were defined on the image onto the container.
func (r *runner) addImageDefinedLabels(image string, labels map[string]string) error {
	cli := dockerClient()
	defer cli.Close()

	ins, _, err := cli.ImageInspectWithRaw(context.Background(), image)
	if err != nil {
		return xerrors.Errorf("failed to inspect %v: %w", image, err)
	}

	for k, v := range ins.ContainerConfig.Labels {
		if !strings.HasPrefix(k, sailLabel) {
			continue
		}

		labels[k] = v
	}

	return nil
}

func (r *runner) stripDuplicateMounts(mounts []mount.Mount) []mount.Mount {
	rmounts := make([]mount.Mount, 0, len(mounts))

	dests := make(map[string]struct{})

	for _, mnt := range mounts {
		if _, ok := dests[mnt.Target]; ok {
			continue
		}
		dests[mnt.Target] = struct{}{}
		rmounts = append(rmounts, mnt)
	}
	return rmounts
}

func panicf(fmtStr string, args ...interface{}) {
	panic(fmt.Sprintf(fmtStr, args...))
}

// resolveMounts replaces ~ with appropriate home paths with
// each mount.
func (r *runner) resolveMounts(mounts []mount.Mount) {
	hostHomeDir, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	for i := range mounts {
		mounts[i].Source, err = filepath.Abs(resolvePath(hostHomeDir, mounts[i].Source))
		if err != nil {
			panicf("failed to resolve %v: %v", mounts[i].Source, err)
		}
		mounts[i].Target = resolvePath(guestHomeDir, mounts[i].Target)
	}
}

func (r *runner) projectDir(image string) (string, error) {
	cli := dockerClient()
	defer cli.Close()

	img, _, err := cli.ImageInspectWithRaw(context.Background(), image)
	if err != nil {
		return "", xerrors.Errorf("failed to inspect image: %w", err)
	}

	proot, ok := img.Config.Labels["project_root"]
	if ok {
		return filepath.Join(proot, r.projectName), nil
	}

	return filepath.Join(guestHomeDir, r.projectName), nil
}

// runnerFromContainer gets a runner from container named
// name.
func runnerFromContainer(name, network string) (*runner, error) {
	cli := dockerClient()
	defer cli.Close()

	ctx := context.Background()
	cnt, err := cli.ContainerInspect(ctx, name)
	if err != nil {
		return nil, xerrors.Errorf("failed to inspect %v: %w", name, err)
	}
	r := &runner{
		cntName:         name,
		hostname:        cnt.Config.Hostname,
		projectLocalDir: cnt.Config.Labels[projectLocalDirLabel],
		projectName:     cnt.Config.Labels[projectNameLabel],
		hostUser:        cnt.Config.User,
		network:         network,
	}

	r.ip, err = dockutil.ContainerIP(ctx, cli, name)
	if err != nil {
		return nil, xerrors.Errorf("failed to get container %s IP: %w", name, err)
	}

	return r, nil
}
