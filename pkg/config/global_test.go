package config

import (
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// The file of global variables is read at every start. A line that is not a
// variable must be passed over: stopping on it stops the service again at each
// restart, with the same file. A value keeps the equal signs it holds.
func TestInitGlobalReadsWhatItCanOfTheFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	content := `OPENAI_API_KEY=c2VjcmV0==
a line with no equal sign
=a value with no name

TZ=Europe/Paris
`
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	path, global := AppManagementGlobalEnvFilePath, Global
	AppManagementGlobalEnvFilePath, Global = envFile, map[string]string{}
	defer func() { AppManagementGlobalEnvFilePath, Global = path, global }()

	InitGlobal("")

	want := map[string]string{"OPENAI_API_KEY": "c2VjcmV0==", "TZ": "Europe/Paris"}
	if len(Global) != len(want) {
		t.Errorf("read %v, want %v", Global, want)
	}
	for key, value := range want {
		if Global[key] != value {
			t.Errorf("%s is %q, want %q", key, Global[key], value)
		}
	}
}

// A value may be long (a certificate, a key in base64): the variables written
// after it are still read, or the next save would drop them from the file.
func TestInitGlobalReadsPastAVeryLongLine(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	long := strings.Repeat("x", 70<<10)
	content := "BIG=" + long + `
TZ=Europe/Paris
`
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	path, global := AppManagementGlobalEnvFilePath, Global
	AppManagementGlobalEnvFilePath, Global = envFile, map[string]string{}
	defer func() { AppManagementGlobalEnvFilePath, Global = path, global }()

	InitGlobal("")

	if Global["BIG"] != long || Global["TZ"] != "Europe/Paris" {
		t.Errorf("read %d variables, BIG of %d bytes, TZ %q", len(Global), len(Global["BIG"]), Global["TZ"])
	}
}

// Requests change the variables while the save that follows each change, and
// every app that starts, read them. Go stops the whole process on a map read
// and written at once, so this test is to be run with -race.
func TestTheVariablesAreChangedWhileTheyAreSavedAndRead(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	path, global := AppManagementGlobalEnvFilePath, Global
	AppManagementGlobalEnvFilePath, Global = envFile, map[string]string{}
	defer func() { AppManagementGlobalEnvFilePath, Global = path, global }()

	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				key := "KEY_" + strconv.Itoa(worker) + "_" + strconv.Itoa(i)
				SetGlobal(key, "a=b")
				if err := SaveGlobal(); err != nil {
					t.Error(err)
				}
				for range Globals() {
				}
				if i%2 == 1 {
					DeleteGlobal(key)
				}
			}
		}()
	}
	wg.Wait()
	if err := SaveGlobal(); err != nil {
		t.Fatal(err)
	}

	// what the last save left is what the next start reads
	want := Globals()
	Global = map[string]string{}
	InitGlobal("")
	if !maps.Equal(Global, want) {
		t.Errorf("read back %d variables, saved %d", len(Global), len(want))
	}
	if len(want) != 100 {
		t.Errorf("%d variables kept, want 100", len(want))
	}
	if entries, _ := os.ReadDir(filepath.Dir(envFile)); len(entries) != 1 {
		t.Errorf("%d files beside the env file, want it alone", len(entries)-1)
	}
}
