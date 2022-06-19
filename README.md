# Dracu
Dracu \=\= Docker Run as Current User

Dracu is a Docker run command wrapper that allows for running a command inside indicated Docker image
using current user and mounting current folder to the container as current working directory.

This is a perfect tool for e.g. building a project in Docker container
without root privileges and using current folder as input and storing
build result in this folder as well.

## Examples

```console
godfryd@rivendel:~/repos/dracu $  dracu ubuntu:latest echo 'hello world'
hello world
```
This just spins up a container with Ubuntu and runs there `echo` command.

```console
godfryd@rivendel:~/repos/dracu $  dracu ./dracu ruby:3.1 rake build
/home/godfryd/work/tools/1.18.3/bin/go build -v -race
go: downloading github.com/docker/docker v20.10.17+incompatible
go: downloading github.com/urfave/cli/v2 v2.10.0
go: downloading github.com/docker/go-connections v0.4.0
...
```
This time `dracu` builds itself. Source code from current directory is mounted into `ruby` Docker container
and `rake build` is invoked. Build artifacts are stored and are available on the host.

## Installation

Just go to releses page and grab latest .tar.gz. It contains `dracu` with this readme and license.
