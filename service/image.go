package service

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/pkg/docker"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/docker/docker/api/types/image"
	client2 "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	"go.uber.org/zap"
)

// Try to pull latest image.
//
// It returns `true` if the image is updated.
func (ds *dockerService) PullLatestImage(ctx context.Context, imageName string) (isImageUpdated bool, err error) {
	go PublishEventWrapper(ctx, common.EventTypeImagePullBegin, map[string]string{
		common.PropertyTypeImageName.Name: imageName,
	})

	defer PublishEventWrapper(ctx, common.EventTypeImagePullEnd, map[string]string{
		common.PropertyTypeImageName.Name: imageName,

		// update image update information in the defer func below, instead of here.
		// this because PublishEventWrapper will retrieve the information from context and include all properties in the event.
		//
		// common.PropertyTypeImageUpdated.Name: fmt.Sprint(isImageUpdated),  // <- no need to do it here.
	})

	defer func() {
		// write image updated information as a property back to context, so both current func and external caller can see it
		//
		// Only when the check ran to an answer. A pull that failed -- unreachable
		// registry, pinned image, daemon error -- knows nothing about whether a newer
		// image exists, and "false" is read downstream as proof that none does.
		// Absent is the honest answer, and callers treat it as "unknown".
		if err != nil {
			return
		}

		common.SetProperties(ctx, map[string]string{common.PropertyTypeImageUpdated.Name: fmt.Sprint(isImageUpdated)}) // <- instead, do it here.
	}()

	if strings.HasPrefix(imageName, "sha256:") {
		message := "container uses a pinned image, and cannot be updated"
		go PublishEventWrapper(ctx, common.EventTypeImagePullError, map[string]string{
			common.PropertyTypeImageName.Name: imageName,
			common.PropertyTypeMessage.Name:   message,
		})

		return false, fmt.Errorf("%s", message)
	}

	imageInfo1, err := docker.Image(ctx, imageName)
	if err != nil {
		go PublishEventWrapper(ctx, common.EventTypeImagePullError, map[string]string{
			common.PropertyTypeImageName.Name: imageName,
			common.PropertyTypeMessage.Name:   err.Error(),
		})
		return false, err
	}

	if match, err := docker.CompareDigest(imageName, imageInfo1.RepoDigests); err != nil {
		// do nothing
	} else if match {
		return false, nil
	}

	if err = docker.PullImage(ctx, imageName, func(out io.ReadCloser) {
		pullImageProgress(ctx, out, imageName, 1, 1)
	}); err != nil {
		go PublishEventWrapper(ctx, common.EventTypeImagePullError, map[string]string{
			common.PropertyTypeImageName.Name: imageName,
			common.PropertyTypeMessage.Name:   err.Error(),
		})
		return false, err
	}

	imageInfo2, err := docker.Image(ctx, imageName)
	if err != nil {
		go PublishEventWrapper(ctx, common.EventTypeImagePullError, map[string]string{
			common.PropertyTypeImageName.Name: imageName,
			common.PropertyTypeMessage.Name:   err.Error(),
		})
		return false, err
	}

	isImageUpdated = imageInfo1.ID != imageInfo2.ID
	return isImageUpdated, nil
}

// 删除镜像
func (ds *dockerService) RemoveImage(name string) error {
	cli, err := client2.NewClientWithOpts(client2.FromEnv, client2.WithAPIVersionNegotiation())
	if err != nil {
		return err
	}
	defer cli.Close()
	imageList, err := cli.ImageList(context.Background(), image.ListOptions{})
	if err != nil {
		return err
	}

	imageID := ""

Loop:
	for _, ig := range imageList {
		for _, i := range ig.RepoTags {
			if i == name {
				imageID = ig.ID
				break Loop
			}
		}
	}
	_, err = cli.ImageRemove(context.Background(), imageID, image.RemoveOptions{})
	return err
}

// What an install is busy with, as the dashboard is told in `app:install:step`.
const (
	InstallStepPull    = "pull"
	InstallStepExtract = "extract"
	InstallStepCreate  = "create"
	InstallStepStart   = "start"
)

// downloadShare is how much of a layer its download is, the rest being its unpacking. A
// layer that has arrived is not ready: counted whole, the bar was full for as long as the
// unpacking took, which is minutes for a large image.
const downloadShare = 0.8

// pullLayer is one layer of the image being pulled.
type pullLayer struct {
	current, total int64 // bytes downloaded, and to download: zero until the download starts
	downloaded     bool
	done           bool // unpacked, or on this host already
}

// pullState follows the pull stream of one image, layer by layer.
type pullState struct {
	layers map[string]*pullLayer
}

func newPullState() *pullState {
	return &pullState{layers: map[string]*pullLayer{}}
}

func (p *pullState) observe(message jsonmessage.JSONMessage) {
	switch message.Status {
	case "Pulling fs layer", "Waiting", "Downloading", "Download complete", "Extracting", "Pull complete", "Already exists":
	default:
		return // about the image, not about one of its layers
	}

	layer := p.layers[message.ID]
	if layer == nil {
		layer = &pullLayer{}
		p.layers[message.ID] = layer
	}

	switch message.Status {
	case "Downloading":
		if message.Progress != nil {
			layer.current, layer.total = message.Progress.Current, message.Progress.Total
		}
	case "Download complete", "Extracting":
		// unpacking counts on the same field as the download: its figures are not read
		layer.downloaded = true
		layer.current = layer.total
	// A layer this host already has arrives as `Already exists` INSTEAD of the
	// pulling/complete pair, never alongside it: known and finished at once. Counting it
	// nowhere left the bar at zero for the whole install of an image already on disk.
	case "Pull complete", "Already exists":
		layer.downloaded, layer.done = true, true
		layer.current = layer.total
	}
}

// properties is what `app:install-progress` says about the pull at this point, and false
// while no layer has been announced: no layers means no fraction, not 0/0.
//
// currentImage is 1-based, so the images already done are currentImage-1: image 2 of 3
// half pulled is (1+0.5)/3. Multiplying by currentImage/totalImageNum instead capped the
// first image of two at 50% and then sent the bar back to zero for the second.
func (p *pullState) properties(imageName string, totalImageNum, currentImage int) (map[string]string, bool) {
	if len(p.layers) == 0 {
		return nil, false
	}

	var fraction float32
	var downloaded, size int64
	sizeKnown, allDownloaded := true, true

	for _, layer := range p.layers {
		switch {
		case layer.done:
			fraction++
		case layer.downloaded:
			fraction += downloadShare
		case layer.total > 0:
			// a layer on its way counts for what has arrived of it: counting finished
			// layers only left the bar still for as long as one big layer took
			fraction += downloadShare * float32(layer.current) / float32(layer.total)
		}

		downloaded += layer.current
		size += layer.total
		sizeKnown = sizeKnown && (layer.downloaded || layer.total > 0)
		allDownloaded = allDownloaded && layer.downloaded
	}

	progress := int((float32(currentImage-1) + fraction/float32(len(p.layers))) / float32(totalImageNum) * 100)
	progress = max(0, min(100, progress))

	// Nothing is left to download, whether or not anything was: an image this host
	// already had is not downloading either.
	step := InstallStepPull
	if allDownloaded {
		step = InstallStepExtract
	}

	properties := map[string]string{
		common.PropertyTypeAppProgress.Name:     fmt.Sprint(progress),
		common.PropertyTypeAppInstallStep.Name:  step,
		common.PropertyTypeImageName.Name:       imageName,
		common.PropertyTypeImageIndex.Name:      fmt.Sprint(currentImage),
		common.PropertyTypeImageCount.Name:      fmt.Sprint(totalImageNum),
		common.PropertyTypeImageDownloaded.Name: fmt.Sprint(downloaded),
	}
	// A layer says its size only once its download starts. Until every layer has, the sum
	// is too small to be shown as the size of the image.
	if sizeKnown {
		properties[common.PropertyTypeImageSize.Name] = fmt.Sprint(size)
	}
	return properties, true
}

// installStepProperties is what `app:install-progress` says once the images are here.
func installStepProperties(step string) map[string]string {
	return map[string]string{
		common.PropertyTypeAppProgress.Name:    "100",
		common.PropertyTypeAppInstallStep.Name: step,
	}
}

// PublishInstallStep tells the dashboard what an install moved on to after the pull.
//
// Not in a goroutine, unlike the events around it: the end of the install is published in
// line by a defer, and a step that lands after it opens the card again with nothing left
// to close it.
func PublishInstallStep(ctx context.Context, step string) {
	PublishEventWrapper(ctx, common.EventTypeAppInstallProgress, installStepProperties(step))
}

type Throttler struct {
	InvokeInterval time.Duration
	LastInvokeTime time.Time
}

func NewThrottler(interval time.Duration) *Throttler {
	return &Throttler{
		InvokeInterval: interval,
	}
}

func (t *Throttler) ThrottleFunc(f func()) {
	if time.Since(t.LastInvokeTime) >= t.InvokeInterval {
		f()
		t.LastInvokeTime = time.Now()
	}
}

func pullImageProgress(ctx context.Context, out io.ReadCloser, imageName string, totalImageNum int, currentImage int) {
	state := newPullState()
	decoder := json.NewDecoder(out)
	if decoder == nil {
		logger.Error("failed to create json decoder")
		return
	}

	// The first frame waits its turn too. The daemon names the layers one line at a time,
	// and a first layer already on this host is, alone, an image that is all here: the bar
	// opened at 100% and came back down as the others were named.
	throttler := NewThrottler(500 * time.Millisecond)
	throttler.LastInvokeTime = time.Now()

	for decoder.More() {
		var message jsonmessage.JSONMessage
		if err := decoder.Decode(&message); err != nil {
			logger.Error("failed to decode json message", zap.Error(err))
			continue
		}

		state.observe(message)

		properties, ok := state.properties(imageName, totalImageNum, currentImage)
		if !ok {
			continue
		}

		// reduce the event send frequency
		throttler.ThrottleFunc(func() {
			go PublishEventWrapper(ctx, common.EventTypeAppInstallProgress, properties)
		})
	}
}
