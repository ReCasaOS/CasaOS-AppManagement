package service

import (
	stdjson "encoding/json"
	"strings"
	"testing"
)

// pullProgressFrom replays a daemon pull stream through the same counting the publisher
// does, and reports the percentages it would have announced.
func pullProgressFrom(t *testing.T, statuses []string, totalImageNum, currentImage int) []int {
	t.Helper()

	lines := make([]string, 0, len(statuses))
	for _, status := range statuses {
		encoded, err := stdjson.Marshal(map[string]string{"status": status})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(encoded))
	}

	layerNum, completedLayerNum := 0, 0
	announced := []int{}

	decoder := stdjson.NewDecoder(strings.NewReader(strings.Join(lines, "\n")))
	for decoder.More() {
		var message struct {
			Status string `json:"status"`
		}
		if err := decoder.Decode(&message); err != nil {
			t.Fatal(err)
		}

		switch message.Status {
		case string(Pull):
			layerNum++
		case string(PullComplete):
			completedLayerNum++
		case string(AlreadyExists):
			layerNum++
			completedLayerNum++
		}

		if layerNum == 0 {
			continue
		}

		completedFraction := float32(completedLayerNum) / float32(layerNum)
		progress := (float32(currentImage-1) + completedFraction) / float32(totalImageNum) * 100
		announced = append(announced, int(progress))
	}

	return announced
}

// An image this host already has is announced layer by layer as `Already exists`, and
// never as the pulling/complete pair. Counting only that pair left every such install
// dividing zero by zero: the bar sat at 0 from the first frame to the last, on the
// installs that finish fastest.
func TestProgressOfAnImageAlreadyOnDisk(t *testing.T) {
	cached := []string{"Already exists", "Already exists", "Already exists"}

	announced := pullProgressFrom(t, cached, 1, 1)
	if len(announced) != len(cached) {
		t.Fatalf("every layer must move the bar, got %v", announced)
	}
	for i, progress := range announced {
		if progress != 100 {
			t.Fatalf("a layer already on disk is a layer that is done: step %d announced %d%%", i, progress)
		}
	}
}

// The bar must not go backwards between the images of a multi-service stack, and must
// not stop short of the end on the last one.
func TestProgressAcrossTheImagesOfAStack(t *testing.T) {
	oneImage := []string{
		"Pulling fs layer", "Pulling fs layer",
		"Pull complete", "Pull complete",
	}

	first := pullProgressFrom(t, oneImage, 2, 1)
	second := pullProgressFrom(t, oneImage, 2, 2)

	if got := first[len(first)-1]; got != 50 {
		t.Fatalf("the first of two images finishes half the job, got %d%%", got)
	}
	if got := second[0]; got < 50 {
		t.Fatalf("the second image starts where the first left off, got %d%%", got)
	}
	if got := second[len(second)-1]; got != 100 {
		t.Fatalf("the last image finishes the job, got %d%%", got)
	}

	// and it only ever moves forward
	for _, run := range [][]int{first, second} {
		for i := 1; i < len(run); i++ {
			if run[i] < run[i-1] {
				t.Fatalf("progress went backwards: %v", run)
			}
		}
	}
}

// A stream that says nothing about layers announces nothing, rather than a fraction
// computed from no layers at all.
func TestProgressSaysNothingBeforeTheFirstLayer(t *testing.T) {
	if announced := pullProgressFrom(t, []string{"Pulling from library/nginx", "Waiting"}, 1, 1); len(announced) != 0 {
		t.Fatalf("nothing is known yet, so nothing may be announced, got %v", announced)
	}
}
