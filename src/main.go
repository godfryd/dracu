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

	"github.com/urfave/cli/v2"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

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

func runContainer(imageName string, command []string, user *user.User, homeDir, workDir string, persistHome bool) int {
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

	mounts := []mount.Mount{
		{
			Type:   mount.TypeBind,
			Source: workDir,
			Target: destWorkDir,
		},
	}

	var vol types.Volume
	if homeDir == "" {
		volOwner := fmt.Sprintf("uid=%s,gid=%s", user.Uid, user.Gid)
		vol, err = cli.VolumeCreate(ctx, volume.VolumeCreateBody{
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

		fmt.Printf("home volumen created: %s\n", vol.Name)

		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeVolume,
			Source: vol.Name,
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
			Cmd:        command,
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

	out, err := cli.ContainerLogs(ctx, resp.ID, types.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		panic(err)
	}

	stdcopy.StdCopy(os.Stdout, os.Stderr, out)

	rmOpts := types.ContainerRemoveOptions{
		RemoveVolumes: true,
		Force:         true,
	}
	err = cli.ContainerRemove(ctx, resp.ID, rmOpts)
	if err != nil {
		panic(err)
	}

	if vol.Name != "" {
		if persistHome {
			fmt.Printf("home volume left: %v\n", vol.Name)
		} else {
			fmt.Printf("deleting home volume: %v\n", vol.Name)
			err = cli.VolumeRemove(ctx, vol.Name, true)
			if err != nil {
				panic(err)
			}
		}
	}

	return int(status.StatusCode)
}

func main() {
	var workDir string
	var homeDir string
	var persistHome bool

	app := &cli.App{
		Usage: "Docker run as current user",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "work-dir",
				Value:       ".",
				Usage:       "directory mounted to the container and used as a current working directory",
				Destination: &workDir,
			},
			&cli.StringFlag{
				Name:        "home-dir",
				Value:       "",
				Usage:       "directory mounted to the container and used as a home directory for current user",
				Destination: &homeDir,
			},
			&cli.BoolFlag{
				Name:        "persist-home",
				Value:       false,
				Usage:       "if home-dir is not provided and this flag is provided then non-persistent volumen is used for home directory",
				Destination: &persistHome,
			},
		},
		Action: func(c *cli.Context) error {
			imageName := c.Args().First()
			if imageName == "" {
				cli.ShowAppHelp(c)
				//fmt.Println("\nerror: missing image name")
				return cli.Exit("\nerror: missing image name", 1)
			}

			fmt.Println("homeDir:", homeDir)
			fmt.Println("workDir:", workDir)
			fmt.Println("persistHome:", persistHome)

			fmt.Println("imageName:", imageName)

			cmd := c.Args().Tail()
			fmt.Println("cmd:", cmd)

			currentUser, err := user.Current()
			if err != nil {
				log.Fatalf(err.Error())
			}
			fmt.Printf("currentUser %v\n", currentUser)

			exitCode := runContainer(imageName, cmd, currentUser, homeDir, workDir, persistHome)

			if exitCode > 0 {
				return cli.Exit(
					fmt.Sprintf("command exited with %d exit code", exitCode),
					exitCode)
			}

			return nil
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}
