package config

import (
	"bytes"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ReCasaOS/CasaOS-AppManagement/common"
	"github.com/ReCasaOS/CasaOS-AppManagement/model"
	"github.com/ReCasaOS/CasaOS-Common/utils/constants"
	"gopkg.in/ini.v1"
)

var (
	CommonInfo = &model.CommonModel{
		RuntimePath: constants.DefaultRuntimePath,
	}

	AppInfo = &model.APPModel{
		AppStorePath: filepath.Join(constants.DefaultDataPath, "appstore"),
		AppsPath:     filepath.Join(constants.DefaultDataPath, "apps"),
		DataPath:     constants.DefaultDataPath,
		LogPath:      constants.DefaultLogPath,
		LogSaveName:  common.AppManagementServiceName,
		LogFileExt:   "log",

		LogsPath:          "/DATA/AppData/casaos/logs",
		LogsRetentionDays: 7,
		LogsMaxSizeMB:     2048,
	}

	ServerInfo = &model.ServerModel{
		AppStoreList: []string{},
	}

	// Global is a map to inject environment variables to the app. Requests change it while
	// the save after each change and every app that starts read it: outside this package,
	// go through SetGlobal, DeleteGlobal and Globals.
	Global   = make(map[string]string)
	globalMu sync.RWMutex
	saveMu   sync.Mutex

	CasaOSGlobalVariables = &model.CasaOSGlobalVariables{}

	Cfg               *ini.File
	ConfigFilePath    string
	GlobalEnvFilePath string
)

func ReloadConfig() {
	var err error
	Cfg, err = ini.LoadSources(ini.LoadOptions{Insensitive: true, AllowShadows: true}, ConfigFilePath)
	if err != nil {
		fmt.Println("failed to reload config", err)
	} else {
		mapTo("common", CommonInfo)
		mapTo("app", AppInfo)
		mapTo("server", ServerInfo)
	}
}

func InitSetup(config string, sample string) {
	ConfigFilePath = AppManagementConfigFilePath
	if len(config) > 0 {
		ConfigFilePath = config
	}

	// create default config file if not exist
	if _, err := os.Stat(ConfigFilePath); os.IsNotExist(err) {
		fmt.Println("config file not exist, create it")
		// create config file
		file, err := os.Create(ConfigFilePath)
		if err != nil {
			panic(err)
		}
		defer file.Close()

		// write default config
		_, err = file.WriteString(sample)
		if err != nil {
			panic(err)
		}
	}

	var err error

	Cfg, err = ini.LoadSources(ini.LoadOptions{Insensitive: true, AllowShadows: true}, ConfigFilePath)
	if err != nil {
		panic(err)
	}

	mapTo("common", CommonInfo)
	mapTo("app", AppInfo)
	mapTo("server", ServerInfo)
}

func SaveSetup() error {
	reflectFrom("common", CommonInfo)
	reflectFrom("app", AppInfo)
	reflectFrom("server", ServerInfo)

	return Cfg.SaveTo(ConfigFilePath)
}

func InitGlobal(config string) {
	// read file
	// file content like this:
	// OPENAI_API_KEY=123456

	// read file
	GlobalEnvFilePath = AppManagementGlobalEnvFilePath
	if len(config) > 0 {
		ConfigFilePath = config
	}

	// from file read key and value
	// set to Global
	// the whole file at once: a line reader gives up at its first line over 64 KiB, and the variables after it were lost
	content, err := os.ReadFile(GlobalEnvFilePath)
	// there can't to panic err. because the env file is a new file
	// very much user didn't have the file.
	if err != nil {
		// log.Fatal will exit the program. So we only can to log the error.
		log.Println("open global env file error:", err)
		return
	}
	globalMu.Lock()
	defer globalMu.Unlock()
	for _, line := range strings.Split(string(content), "\n") {
		// a line that is not key=value is passed over; the value is all that follows the first "="
		if key, value, ok := strings.Cut(strings.TrimSuffix(line, "\r"), "="); ok && key != "" {
			Global[key] = value
		}
	}
}

func SetGlobal(key, value string) {
	globalMu.Lock()
	defer globalMu.Unlock()
	Global[key] = value
}

func DeleteGlobal(key string) {
	globalMu.Lock()
	defer globalMu.Unlock()
	delete(Global, key)
}

// Globals is a copy of the variables: the caller reads it while requests change them.
func Globals() map[string]string {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return maps.Clone(Global)
}

func SaveGlobal() error {
	// one save at a time, each from the variables as they are when it runs: the last to run leaves the file up to date
	saveMu.Lock()
	defer saveMu.Unlock()

	// file content like this:
	// OPENAI_API_KEY=123456
	var content bytes.Buffer
	for key, value := range Globals() {
		fmt.Fprintf(&content, "%s=%s\n", key, value)
	}

	// written in place: the file keeps the mode it was given, and may be a link or a file mounted on its own
	return os.WriteFile(AppManagementGlobalEnvFilePath, content.Bytes(), 0o644)
}

func mapTo(section string, v interface{}) {
	err := Cfg.Section(section).MapTo(v)
	if err != nil {
		log.Fatalf("Cfg.MapTo %s err: %v", section, err)
	}
}

func reflectFrom(section string, v interface{}) {
	err := Cfg.Section(section).ReflectFrom(v)
	if err != nil {
		log.Fatalf("Cfg.ReflectFrom %s err: %v", section, err)
	}
}
