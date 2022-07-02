package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	//"io/ioutil"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	//"github.com/urfave/cli/v2"
	flags "github.com/jessevdk/go-flags"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/system"
)

var DracuVersion string

type authCfg struct {
	Auth          string `json:"auth"`
	Email         string `json:"email"`
	IdentityToken string `json:"identitytoken"`
	RegistryToken string `json:"registrytoken"`
}

func parseDockerConfig(r io.Reader) (map[string]authCfg, error) {
	buf := new(bytes.Buffer)
	buf.ReadFrom(r)
	byteData := buf.Bytes()

	confsWrapper := struct {
		Auths map[string]authCfg `json:"auths"`
	}{}
	if err := json.Unmarshal(byteData, &confsWrapper); err == nil {
		if len(confsWrapper.Auths) > 0 {
			return confsWrapper.Auths, nil
		}
	}

	var confs map[string]authCfg
	if err := json.Unmarshal(byteData, &confs); err != nil {
		return nil, err
	}
	return confs, nil
}


func resolveLocalPath(localPath string) (absPath string, err error) {
	if absPath, err = filepath.Abs(localPath); err != nil {
		return
	}
	return archive.PreserveTrailingDotOrSeparator(absPath, localPath, filepath.Separator), nil
}

func copyToContainer(ctx context.Context, client *client.Client, cntrID string, srcPath, dstPath string) (err error) {
	// Get an absolute source path.
	srcPath, err = resolveLocalPath(srcPath)
	if err != nil {
		return err
	}

	// Prepare destination copy info by stat-ing the container path.
	dstInfo := archive.CopyInfo{Path: dstPath}
	dstStat, err := client.ContainerStatPath(ctx, cntrID, dstPath)

	// If the destination is a symbolic link, we should evaluate it.
	if err == nil && dstStat.Mode&os.ModeSymlink != 0 {
		linkTarget := dstStat.LinkTarget
		if !system.IsAbs(linkTarget) {
			// Join with the parent directory.
			dstParent, _ := archive.SplitPathDirEntry(dstPath)
			linkTarget = filepath.Join(dstParent, linkTarget)
		}

		dstInfo.Path = linkTarget
		dstStat, err = client.ContainerStatPath(ctx, cntrID, linkTarget)
	}

	// Ignore any error and assume that the parent directory of the destination
	// path exists, in which case the copy may still succeed. If there is any
	// type of conflict (e.g., non-directory overwriting an existing directory
	// or vice versa) the extraction will fail. If the destination simply did
	// not exist, but the parent directory does, the extraction will still
	// succeed.
	if err == nil {
		dstInfo.Exists, dstInfo.IsDir = true, dstStat.Mode.IsDir()
	}

	// Prepare source copy info.
	srcInfo, err := archive.CopyInfoSourcePath(srcPath, false)
	if err != nil {
		return err
	}

	srcArchive, err := archive.TarResource(srcInfo)
	if err != nil {
		return err
	}
	defer srcArchive.Close()

	dstInfo.Exists = false

	// destination that the user specified.
	resolvedDstPath, preparedArchive, err := archive.PrepareArchiveCopy(srcArchive, srcInfo, dstInfo)
	if err != nil {
		return err
	}
	defer preparedArchive.Close()

	options := types.CopyToContainerOptions{
		AllowOverwriteDirWithFile: false,
		CopyUIDGID:                false,
	}

	res := client.CopyToContainer(ctx, cntrID, resolvedDstPath, preparedArchive, options)

	return res
}


func runContainer(imageName string, command []string, user *user.User, homeDir, workDir string, persistHome, volatileWork bool) int {
	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}

	imgPullOpts := types.ImagePullOptions{}

	if strings.Contains(imageName, "/") {
		dkrCfgPath := filepath.Join(user.HomeDir, ".docker", "config.json")
		if _, err := os.Stat(dkrCfgPath); err == nil {
			dkrCfgFp, err := os.Open(dkrCfgPath)
			if err != nil {
				panic(err)
			}
			defer dkrCfgFp.Close()

			auths, err := parseDockerConfig(dkrCfgFp)
			if err == nil {
				baseUrl := strings.Split(imageName, "/")[0]
				for url, cfg := range auths {
					if url == baseUrl {
						imgPullOpts.RegistryAuth = cfg.Auth
						break
					}
				}
			}
		}

	} else {
		imageName = "docker.io/library/" + imageName
	}

	reader, err := cli.ImagePull(ctx, imageName, imgPullOpts)
	if err != nil {
		panic(err)
	}

	defer reader.Close()
	io.Copy(os.Stdout, reader)

	userStr := fmt.Sprintf("%s:%s", user.Uid, user.Gid)
	fmt.Printf("user: %s\n", userStr)

	workDir, err = filepath.Abs(workDir)
	if err != nil {
		panic(err)
	}
	destWorkDir := filepath.Join(user.HomeDir, "work")

	mounts := []mount.Mount{}

	var workVol types.Volume
	if !volatileWork {
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: workDir,
			Target: destWorkDir,
		})
	} else {
		volOwner := fmt.Sprintf("uid=%s,gid=%s", user.Uid, user.Gid)
		workVol, err = cli.VolumeCreate(ctx, volume.VolumeCreateBody{
			Driver: "local",
			DriverOpts: map[string]string{
				"type":   "tmpfs",
				"device": "tmpfs",
				"o":      volOwner,
			},
		})
		if err != nil {
			panic(err)
		}

		fmt.Printf("work volumen created: %s\n", workVol.Name)

		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeVolume,
			Source: workVol.Name,
			Target: destWorkDir,
		})
	}

	var homeVol types.Volume
	if homeDir == "" {
		volOwner := fmt.Sprintf("uid=%s,gid=%s", user.Uid, user.Gid)
		homeVol, err = cli.VolumeCreate(ctx, volume.VolumeCreateBody{
			//Name:   "dkrme-home",
			Driver: "local",
			DriverOpts: map[string]string{
				"type":   "tmpfs",
				"device": "tmpfs",
				"o":      volOwner,
			},
		})
		if err != nil {
			panic(err)
		}

		fmt.Printf("home volumen created: %s\n", homeVol.Name)

		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeVolume,
			Source: homeVol.Name,
			Target: user.HomeDir,
		})
	} else {
		homeDir, err = filepath.Abs(homeDir)
		if err != nil {
			panic(err)
		}
		fmt.Printf("mount host directory %s as home\n", homeDir)
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: homeDir,
			Target: user.HomeDir,
		})
	}

	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image:      imageName,
			Cmd:        []string{"sleep", "100000"},
			Tty:        false,
			User:       userStr,
			WorkingDir: destWorkDir,
		},
		&container.HostConfig{
			Binds: []string{
				"/etc/passwd:/etc/passwd:ro",
				"/etc/group:/etc/group:ro",
			},
			Mounts: mounts,
		},
		nil, nil, "")
	if err != nil {
		panic(err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		panic(err)
	}

	if volatileWork {
		fmt.Printf("copy workdir from %s to %s\n", workDir, destWorkDir)
		err = copyToContainer(ctx, cli, resp.ID, workDir, destWorkDir)
		if err != nil {
			panic(err)
		}
	}

	// RUN COMMAND
	idResponse, err := cli.ContainerExecCreate(ctx, resp.ID,types.ExecConfig{
		User: user.Uid,
		WorkingDir: destWorkDir,
		Cmd: command,
		Tty:true,
		AttachStderr:true,
		AttachStdout:true,
		AttachStdin:true,
		Detach:true,
	})
	attach, err := cli.ContainerExecAttach(ctx, idResponse.ID, types.ExecStartCheck{})
	if err != nil {
		panic(err)
	}
	defer attach.Close()
	stdcopy.StdCopy(os.Stdout, os.Stderr, attach.Reader)

	timeout := time.Duration(10)
	if err := cli.ContainerStop(ctx, resp.ID, &timeout); err != nil {
		fmt.Println("error when stop container ", err)
	}

	// WAIT FOR FINISH
	statusCh, errCh := cli.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	var status container.ContainerWaitOKBody
	select {
	case err := <-errCh:
		if err != nil {
			panic(err)
		}
	case status = <-statusCh:
		if status.Error != nil {
			fmt.Printf("error: %s\n", status.Error.Message)
		}
	}

	rmOpts := types.ContainerRemoveOptions{
		RemoveVolumes: true,
		Force:         true,
	}
	err = cli.ContainerRemove(ctx, resp.ID, rmOpts)
	if err != nil {
		panic(err)
	}

	if homeVol.Name != "" {
		if persistHome {
			fmt.Printf("home volume left: %v\n", homeVol.Name)
		} else {
			fmt.Printf("deleting home volume: %v\n", homeVol.Name)
			err = cli.VolumeRemove(ctx, homeVol.Name, true)
			if err != nil {
				panic(err)
			}
		}
	}

	if workVol.Name != "" {
		fmt.Printf("deleting work volume: %v\n", workVol.Name)
		err = cli.VolumeRemove(ctx, workVol.Name, true)
		if err != nil {
			panic(err)
		}
	}

	return int(status.StatusCode)
}

type AppOpts struct {
	Verbose     bool   `short:"v" long:"verbose" description:"Verbose"`
	Version     bool   `long:"version" description:"Version"`
	WorkDir     string `long:"work-dir" description:"directory mounted to the container and used as a current working directory" default:"."`
	HomeDir     string `long:"home-dir" description:"directory mounted to the container and used as a home directory for current user"`
	PersistHome bool   `long:"persist-home" description:"if home-dir is not provided and this flag is provided then non-persistent volumen is used for home directory"`
	VolatileWork bool  `long:"volatile-work" description:"if enabled then any changes made to work-dir inside a containerare not copied back to the host"`
	Args        struct {
		ImageName string    `positional-arg-name:"<docker-image-name>"`
		Rest      []string  `positional-arg-name:"<command-with-args>"`
	} `positional-args:"yes"`
}


func startApp(opts AppOpts) int {
	imageName := opts.Args.ImageName

	fmt.Println("homeDir:", opts.HomeDir)
	fmt.Println("workDir:", opts.WorkDir)
	fmt.Println("persistHome:", opts.PersistHome)

	fmt.Println("imageName:", imageName)

	cmd := opts.Args.Rest
	fmt.Println("cmd:", cmd)

	currentUser, err := user.Current()
	if err != nil {
		log.Fatalf(err.Error())
	}
	fmt.Printf("currentUser %v\n", currentUser)

	exitCode := runContainer(imageName, cmd, currentUser, opts.HomeDir, opts.WorkDir, opts.PersistHome, opts.VolatileWork)

	fmt.Sprintf("command exited with %d exit code", exitCode)

	return exitCode
}

func main() {
	if DracuVersion == "" {
		DracuVersion = "0.0.0"
	}

	var opts AppOpts

	parser := flags.NewParser(&opts, flags.Default)
	parser.LongDescription = `Docker Run as Current User

It runs a provided <command-with-args> inside a Docker container
run using Docker image indicated in <docker-image-name> argument.
The user used inside Docker container is current user.`

	// Parse options.
	if _, err := parser.Parse(); err != nil {
		ferr := err.(*flags.Error)
		if ferr.Type == flags.ErrHelp {
			os.Exit(0)
		} else {
			log.Fatal(err.Error())
		}
	}

	if opts.Version {
		fmt.Printf("Dracu version: %s\n", DracuVersion)
		os.Exit(0)
	}

	if opts.Args.ImageName == "" {
		parser.WriteHelp(os.Stdout)
		fmt.Println("\nerror: missing image name")
		os.Exit(1)
	}


	if len(opts.Args.Rest) == 0 {
		parser.WriteHelp(os.Stdout)
		fmt.Println("\nerror: missing command to execute in a container")
		os.Exit(1)
	}

	exitCode := startApp(opts)
	os.Exit(exitCode)
}
