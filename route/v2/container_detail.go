package v2

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/ReCasaOS/CasaOS-AppManagement/codegen"
	"github.com/ReCasaOS/CasaOS-AppManagement/service"
	"github.com/ReCasaOS/CasaOS-Common/utils"
	"github.com/docker/docker/api/types"
	"github.com/labstack/echo/v4"
)

// What a container is, for somebody who did not create it.
//
// Docker hands out `adoring_antonelli`, and a container whose name it never set
// falls back to a 64-character id. The dashboard had no way to answer "what is
// this", and no way to get rid of it either -- so this answers the first question
// and the delete below answers the second.

// ContainerDetail describes one container.
func (a *AppManagement) ContainerDetail(ctx echo.Context, id codegen.ContainerID) error {
	inspected, err := service.MyService.Docker().DescribeContainer(ctx.Request().Context(), string(id))
	if err != nil {
		message := err.Error()
		if strings.Contains(strings.ToLower(message), "no such container") {
			return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &message})
		}

		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	detail := describeContainer(ctx.Request().Context(), inspected)

	return ctx.JSON(http.StatusOK, codegen.ContainerDetailOK{
		Message: utils.Ptr("OK"), Data: &detail,
	})
}

// DeleteContainer removes a container and, if asked and if allowed, some of its
// named volumes.
func (a *AppManagement) DeleteContainer(ctx echo.Context, id codegen.ContainerID) error {
	var request struct {
		Volumes *[]string `json:"volumes"`
	}
	// A body is optional here: deleting a container and none of its volumes is the
	// common case and should not need one.
	_ = ctx.Bind(&request)

	inspected, err := service.MyService.Docker().DescribeContainer(ctx.Request().Context(), string(id))
	if err != nil {
		message := err.Error()
		if strings.Contains(strings.ToLower(message), "no such container") {
			return ctx.JSON(http.StatusNotFound, codegen.ResponseNotFound{Message: &message})
		}

		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	// Refused outright rather than made to work: taking one service out of a
	// compose project through this door leaves the project in a state its own
	// manager -- whether that is CasaOS, Portainer or a person with a terminal --
	// does not expect. The app's own uninstall is the way to remove a stack.
	if project := composeProjectOf(inspected); project != "" {
		message := fmt.Sprintf("this container belongs to the compose project `%s`; remove the app rather than one of its containers", project)
		return ctx.JSON(http.StatusBadRequest, codegen.ResponseBadRequest{Message: &message})
	}

	wanted := []string{}
	if request.Volumes != nil {
		wanted = *request.Volumes
	}

	// Decided again here against what the daemon says NOW, never against what the
	// screen was drawn from.
	remove, refused := service.VolumesToRemove(namedVolumesOf(ctx.Request().Context(), inspected), wanted)

	if err := service.MyService.Docker().RemoveContainer(string(id), false); err != nil {
		message := err.Error()
		return ctx.JSON(http.StatusInternalServerError, codegen.ResponseInternalServerError{Message: &message})
	}

	removed := []string{}
	for _, name := range remove {
		if err := service.MyService.Docker().RemoveVolume(ctx.Request().Context(), name); err != nil {
			// The container is already gone, so this is not a failure of the whole
			// operation -- but it is not a success either, and saying which volumes
			// survived beats a green tick over a disk that did not shrink.
			refused[name] = err.Error()
			continue
		}

		removed = append(removed, name)
	}

	return ctx.JSON(http.StatusOK, codegen.ContainerRemovedOK{
		Message: utils.Ptr(fmt.Sprintf("container `%s` removed", id)),
		Data: &struct {
			Refused *map[string]string `json:"refused,omitempty"`
			Removed *[]string          `json:"removed,omitempty"`
		}{Removed: &removed, Refused: &refused},
	})
}

func composeProjectOf(inspected *types.ContainerJSON) string {
	if inspected == nil || inspected.Config == nil {
		return ""
	}

	return inspected.Config.Labels["com.docker.compose.project"]
}

// namedVolumesOf reads the container's named volumes and asks the daemon what it
// knows about each.
func namedVolumesOf(ctx context.Context, inspected *types.ContainerJSON) []service.ContainerVolume {
	volumes := []service.ContainerVolume{}
	if inspected == nil {
		return volumes
	}

	usage := service.MyService.Docker().VolumeUsage(ctx)

	for _, mount := range inspected.Mounts {
		if mount.Type != "volume" {
			continue
		}

		// Unknown until the daemon says otherwise, which the decision reads as
		// "do not offer".
		space := service.ContainerVolume{Name: mount.Name, Target: mount.Destination, Size: -1, Containers: -1}
		if known, ok := usage[mount.Name]; ok {
			space.Size = known.Size
			space.Containers = known.Containers
		}

		volumes = append(volumes, space)
	}

	return volumes
}

func describeContainer(ctx context.Context, inspected *types.ContainerJSON) codegen.ContainerDetail {
	detail := codegen.ContainerDetail{}
	if inspected == nil {
		return detail
	}

	name := strings.TrimPrefix(inspected.Name, "/")
	detail.Id = &inspected.ID
	detail.Name = &name

	if inspected.State != nil {
		detail.State = &inspected.State.Status
	}

	if inspected.Config != nil {
		detail.Image = &inspected.Config.Image
		command := strings.Join(inspected.Config.Cmd, " ")
		detail.Command = &command

		env := append([]string{}, inspected.Config.Env...)
		sort.Strings(env)
		detail.Env = &env

		if project := inspected.Config.Labels["com.docker.compose.project"]; project != "" {
			detail.ComposeProject = &project
		}
	}

	if inspected.HostConfig != nil {
		policy := string(inspected.HostConfig.RestartPolicy.Name)
		detail.RestartPolicy = &policy
	}

	networks := []string{}
	ports := []codegen.ContainerPortMapping{}
	if inspected.NetworkSettings != nil {
		for network := range inspected.NetworkSettings.Networks {
			networks = append(networks, network)
		}
		sort.Strings(networks)

		for port, bindings := range inspected.NetworkSettings.Ports {
			for _, binding := range bindings {
				host, container, protocol := binding.HostPort, port.Port(), port.Proto()
				ports = append(ports, codegen.ContainerPortMapping{
					Host: &host, Container: &container, Protocol: &protocol,
				})
			}
		}
		// Docker answers in no order, and a panel that reshuffles between two
		// refreshes cannot be read.
		sort.Slice(ports, func(i, j int) bool { return *ports[i].Container < *ports[j].Container })
	}
	detail.Networks = &networks
	detail.Ports = &ports

	binds := []codegen.ContainerBind{}
	for _, mount := range inspected.Mounts {
		if mount.Type == "volume" {
			continue
		}

		source, target, readOnly := mount.Source, mount.Destination, !mount.RW
		binds = append(binds, codegen.ContainerBind{Source: &source, Target: &target, ReadOnly: &readOnly})
	}
	sort.Slice(binds, func(i, j int) bool { return *binds[i].Target < *binds[j].Target })
	detail.Binds = &binds

	described := service.DescribeVolumesForRemoval(namedVolumesOf(ctx, inspected))
	volumes := make([]codegen.ContainerVolume, 0, len(described))
	for _, volume := range described {
		volume := volume
		volumes = append(volumes, codegen.ContainerVolume{
			Name: &volume.Name, Target: &volume.Target,
			Size: &volume.Size, Containers: &volume.Containers,
			Removable: &volume.Removable, Reason: &volume.Reason,
		})
	}
	detail.Volumes = &volumes

	return detail
}
