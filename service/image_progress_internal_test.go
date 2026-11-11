package service

import (
	"fmt"
	"testing"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/docker/docker/pkg/jsonmessage"
)

// layerMessage is one line of a daemon pull stream, about one layer.
func layerMessage(id, status string, current, total int64) jsonmessage.JSONMessage {
	message := jsonmessage.JSONMessage{ID: id, Status: status}
	if total > 0 {
		message.Progress = &jsonmessage.JSONProgress{Current: current, Total: total}
	}
	return message
}

// announcedFrom feeds a pull stream to the real counting and returns what would have been
// published after each line that says something.
func announcedFrom(messages []jsonmessage.JSONMessage, image string, totalImageNum, currentImage int) []map[string]string {
	state := newPullState()
	announced := []map[string]string{}
	for _, message := range messages {
		state.observe(message)
		if properties, ok := state.properties(image, totalImageNum, currentImage); ok {
			announced = append(announced, properties)
		}
	}
	return announced
}

func percentages(announced []map[string]string) []string {
	out := make([]string, 0, len(announced))
	for _, properties := range announced {
		out = append(out, properties[common.PropertyTypeAppProgress.Name])
	}
	return out
}

// twoLayersPulled is the stream of an image of two layers, neither on disk.
func twoLayersPulled() []jsonmessage.JSONMessage {
	return []jsonmessage.JSONMessage{
		layerMessage("a", "Pulling fs layer", 0, 0),
		layerMessage("b", "Pulling fs layer", 0, 0),
		layerMessage("a", "Pull complete", 0, 0),
		layerMessage("b", "Pull complete", 0, 0),
	}
}

// An image this host already has is announced layer by layer as `Already exists`, and
// never as the pulling/complete pair. Counting only that pair left every such install
// dividing zero by zero: the bar sat at 0 from the first frame to the last, on the
// installs that finish fastest.
func TestProgressOfAnImageAlreadyOnDisk(t *testing.T) {
	cached := []jsonmessage.JSONMessage{
		layerMessage("a", "Already exists", 0, 0),
		layerMessage("b", "Already exists", 0, 0),
		layerMessage("c", "Already exists", 0, 0),
	}

	announced := percentages(announcedFrom(cached, "nginx", 1, 1))
	if len(announced) != len(cached) {
		t.Fatalf("every layer must move the bar, got %v", announced)
	}
	for i, progress := range announced {
		if progress != "100" {
			t.Fatalf("a layer already on disk is a layer that is done: step %d announced %s%%", i, progress)
		}
	}
}

// The bar must not go backwards between the images of a multi-service stack, and must
// not stop short of the end on the last one.
func TestProgressAcrossTheImagesOfAStack(t *testing.T) {
	first := percentages(announcedFrom(twoLayersPulled(), "db", 2, 1))
	second := percentages(announcedFrom(twoLayersPulled(), "web", 2, 2))

	if got := first[len(first)-1]; got != "50" {
		t.Fatalf("the first of two images finishes half the job, got %s%%", got)
	}
	if got := second[0]; got != "50" {
		t.Fatalf("the second image starts where the first left off, got %s%%", got)
	}
	if got := second[len(second)-1]; got != "100" {
		t.Fatalf("the last image finishes the job, got %s%%", got)
	}
}

// A stream that says nothing about layers announces nothing, rather than a fraction
// computed from no layers at all.
func TestProgressSaysNothingBeforeTheFirstLayer(t *testing.T) {
	quiet := []jsonmessage.JSONMessage{
		{ID: "latest", Status: "Pulling from library/nginx"},
		{Status: "Digest: sha256:0000"},
	}
	if announced := announcedFrom(quiet, "nginx", 1, 1); len(announced) != 0 {
		t.Fatalf("nothing is known yet, so nothing may be announced, got %v", announced)
	}
}

// Counting finished layers only left the bar still for as long as one big layer took.
// The bytes of a layer on its way count too.
func TestProgressMovesWhileALayerDownloads(t *testing.T) {
	announced := percentages(announcedFrom([]jsonmessage.JSONMessage{
		layerMessage("a", "Pulling fs layer", 0, 0),
		layerMessage("b", "Pulling fs layer", 0, 0),
		layerMessage("a", "Downloading", 50, 100),
	}, "nginx", 1, 1))

	if got := announced[len(announced)-1]; got != "20" {
		t.Fatalf("half the download of one layer out of two, the download being four fifths of a layer, got %s%%", got)
	}
}

// A layer that has arrived is not a layer that is ready: unpacking a large one takes
// minutes, and a bar already full for all of them reads as an install that hung at the end.
// The windows that word 100% as "completed" said so with the containers not yet created.
func TestProgressKeepsTheEndOfTheBarForUnpacking(t *testing.T) {
	announced := percentages(announcedFrom([]jsonmessage.JSONMessage{
		layerMessage("a", "Pulling fs layer", 0, 0),
		layerMessage("a", "Downloading", 50, 100),
		layerMessage("a", "Download complete", 0, 0),
		layerMessage("a", "Extracting", 5, 100),
		layerMessage("a", "Pull complete", 0, 0),
	}, "nginx", 1, 1))

	if got := fmt.Sprint(announced); got != "[0 40 80 80 100]" {
		t.Fatalf("got %s", got)
	}
}

// Once every layer is unpacked nothing is downloading, and nothing was for an image this
// host already had: the step must not fall back to the download.
func TestProgressDoesNotGoBackToDownloadingAtTheEnd(t *testing.T) {
	cached := []jsonmessage.JSONMessage{layerMessage("a", "Already exists", 0, 0)}

	for name, stream := range map[string][]jsonmessage.JSONMessage{"pulled": twoLayersPulled(), "cached": cached} {
		announced := announcedFrom(stream, "nginx", 1, 1)
		if got := announced[len(announced)-1][common.PropertyTypeAppInstallStep.Name]; got == InstallStepPull {
			t.Fatalf("%s: every layer is here and the step still says %q", name, got)
		}
	}
}

// The size of the image is the sum of its layers, and a layer says its size only once its
// download starts. Until every layer has, the sum is too small to be shown as a total.
func TestProgressGivesTheSizeOnlyOnceEveryLayerHasSaidIts(t *testing.T) {
	announced := announcedFrom([]jsonmessage.JSONMessage{
		layerMessage("a", "Pulling fs layer", 0, 0),
		layerMessage("b", "Pulling fs layer", 0, 0),
		layerMessage("a", "Downloading", 40, 100),
		layerMessage("b", "Downloading", 10, 300),
		layerMessage("a", "Download complete", 0, 0),
	}, "nginx", 1, 1)

	oneLayerKnown := announced[2]
	if got := oneLayerKnown[common.PropertyTypeImageDownloaded.Name]; got != "40" {
		t.Fatalf("40 bytes are here, got %q", got)
	}
	if got, ok := oneLayerKnown[common.PropertyTypeImageSize.Name]; ok {
		t.Fatalf("one layer has not said its size, so the image has none yet, got %q", got)
	}

	last := announced[len(announced)-1]
	if got := last[common.PropertyTypeImageDownloaded.Name]; got != "110" {
		t.Fatalf("a downloaded layer counts for its whole size: 100 + 10, got %q", got)
	}
	if got := last[common.PropertyTypeImageSize.Name]; got != "400" {
		t.Fatalf("both layers have said their size: 100 + 300, got %q", got)
	}
}

// Unpacking reports its own current/total on the same field as the download. It is not
// bytes arriving, and the step says which of the two the box is busy with.
func TestProgressTellsUnpackingFromDownloading(t *testing.T) {
	announced := announcedFrom([]jsonmessage.JSONMessage{
		layerMessage("a", "Pulling fs layer", 0, 0),
		layerMessage("a", "Downloading", 60, 100),
		layerMessage("a", "Download complete", 0, 0),
		layerMessage("a", "Extracting", 5, 100),
	}, "nginx", 1, 1)

	if got := announced[1][common.PropertyTypeAppInstallStep.Name]; got != InstallStepPull {
		t.Fatalf("a layer on its way is a download, got %q", got)
	}

	last := announced[len(announced)-1]
	if got := last[common.PropertyTypeAppInstallStep.Name]; got != InstallStepExtract {
		t.Fatalf("everything is here and not everything is unpacked, got %q", got)
	}
	if got := last[common.PropertyTypeImageDownloaded.Name]; got != "100" {
		t.Fatalf("unpacking must not rewind the bytes downloaded, got %q", got)
	}
}

// Which image, and which of how many: a stack of three is three downloads, and the card
// would otherwise show one bar with no word on what it is fetching.
func TestProgressNamesTheImageAndItsPlace(t *testing.T) {
	announced := announcedFrom(twoLayersPulled(), "ghcr.io/owner/web:1.2", 3, 2)

	for _, properties := range announced {
		got := fmt.Sprint(
			properties[common.PropertyTypeImageName.Name], " ",
			properties[common.PropertyTypeImageIndex.Name], "/",
			properties[common.PropertyTypeImageCount.Name],
		)
		if got != "ghcr.io/owner/web:1.2 2/3" {
			t.Fatalf("got %q", got)
		}
	}
}

// The event type is what the bus is told the event carries, and what the dashboard is
// written against: a property sent without being listed there is one nobody was told of.
func TestInstallProgressEventListsWhatItCarries(t *testing.T) {
	listed := map[string]bool{}
	for _, property := range common.EventTypeAppInstallProgress.PropertyTypeList {
		listed[property.Name] = true
	}

	announced := announcedFrom(twoLayersPulled(), "nginx", 1, 1)
	for name := range announced[len(announced)-1] {
		if !listed[name] {
			t.Fatalf("app:install-progress carries %q and does not list it", name)
		}
	}
	for name := range installStepProperties(InstallStepStart) {
		if !listed[name] {
			t.Fatalf("app:install-progress carries %q and does not list it", name)
		}
	}
}
