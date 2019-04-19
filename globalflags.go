package main

import (
	"context"
	"flag"
	"os/exec"

	"github.com/fatih/color"
	"go.coder.com/flog"
	"go.coder.com/sail/internal/dockutil"
)

type globalFlags struct {
	verbose    bool
	configPath string
}

func (gf *globalFlags) debug(msg string, args ...interface{}) {
	if !gf.verbose {
		return
	}
	flog.Log(
		flog.Level(color.New(color.FgHiMagenta).Sprint("DEBUG")),
		msg, args...,
	)
}

func (gf *globalFlags) config() config {
	return mustReadConfig(gf.configPath)
}

// ensureDockerDaemon verifies that Docker is running.
func (gf *globalFlags) ensureDockerDaemon() {
	out, err := exec.Command("docker", "info").CombinedOutput()
	if err != nil {
		flog.Fatal("failed to run `docker info`: %v\n%s", err, out)
	}
	gf.debug("verified Docker is running")
}

func requireRepo(fl *flag.FlagSet) repo {
	repoURI := fl.Arg(0)
	if repoURI == "" {
		flog.Fatal("Argument <repo> must be provided.")
	}

	r, err := ParseRepo(repoURI)
	if err != nil {
		flog.Fatal("failed to parse repo %q: %v", repoURI, err)
	}
	return r
}

// ensureDockerNetwork ensures that the sail network is created.
func (gf *globalFlags) ensureDockerNetwork() {
	cli := dockerClient()
	defer cli.Close()

	err := dockutil.EnsureNetwork(context.Background(), cli, gf.config().DefaultNetwork, gf.config().DefaultSubnet)
	if err != nil {
		flog.Fatal("%v", err)
	}
}

// project reads the project as the first parameter.
func (gf *globalFlags) project(fl *flag.FlagSet) *project {
	return &project{
		gf:   gf,
		conf: gf.config(),
		repo: requireRepo(fl),
	}
}
