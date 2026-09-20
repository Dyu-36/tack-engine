package projects

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/config"
)

const projectsFileName = "projects.json"

type Project struct {
	Path         string    `json:"path"`
	DataDir      string    `json:"data_dir"`
	LastAccessed time.Time `json:"last_accessed"`
}

type ProjectList struct {
	Projects []Project `json:"projects"`
}

var mu sync.Mutex

func projectsFilePath() string {
	return filepath.Join(filepath.Dir(config.GlobalConfigData()), projectsFileName)
}

func Load() (*ProjectList, error) {
	mu.Lock()
	defer mu.Unlock()
	return loadLocked()
}

func loadLocked() (*ProjectList, error) {
	data, err := os.ReadFile(projectsFilePath())
	if errors.Is(err, os.ErrNotExist) {
		return &ProjectList{Projects: []Project{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var list ProjectList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

func Save(list *ProjectList) error {
	mu.Lock()
	defer mu.Unlock()
	return saveLocked(list)
}

func saveLocked(list *ProjectList) error {
	if list == nil {
		return errors.New("nil project list")
	}
	path := projectsFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".projects-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temporary, path)
}

func Register(workingDir, dataDir string) error {
	mu.Lock()
	defer mu.Unlock()
	list, err := loadLocked()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, project := range list.Projects {
		if !now.After(project.LastAccessed) {
			now = project.LastAccessed.Add(time.Nanosecond)
		}
	}
	found := false
	for index, project := range list.Projects {
		if project.Path == workingDir {
			list.Projects[index].DataDir = dataDir
			list.Projects[index].LastAccessed = now
			found = true
			break
		}
	}
	if !found {
		list.Projects = append(list.Projects, Project{Path: workingDir, DataDir: dataDir, LastAccessed: now})
	}
	slices.SortFunc(list.Projects, func(a, b Project) int { return b.LastAccessed.Compare(a.LastAccessed) })
	return saveLocked(list)
}

func List() ([]Project, error) {
	list, err := Load()
	if err != nil {
		return nil, err
	}
	return list.Projects, nil
}
